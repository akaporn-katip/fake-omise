package api

import (
	"crypto/rand"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/akaporn-katip/fake-omise/internal/omise"
	"github.com/akaporn-katip/fake-omise/internal/store"
)

// apiError carries an HTTP status alongside an Omise error body.
type apiError struct {
	status int
	body   *omise.Error
}

func (e *apiError) Error() string { return e.body.Message }

func bad(code, message string) *apiError {
	return &apiError{status: http.StatusBadRequest, body: omise.NewError(code, message)}
}

func missing() *apiError {
	return &apiError{status: http.StatusNotFound, body: omise.NewError("not_found", "the requested object was not found")}
}

// emit persists an event and hands it to the webhook dispatcher.
func (s *Server) emit(key string, data any) *omise.Event {
	if data == nil {
		return nil
	}
	ev, err := s.Store.CreateEvent(false, key, data)
	if err != nil {
		s.Config.Logger.Printf("create event %s: %v", key, err)
		return nil
	}
	s.hooks.Enqueue(ev.ID)
	return ev
}

// createCharge implements POST /charges, the heart of the fake.
func (s *Server) createCharge(w http.ResponseWriter, r *http.Request) {
	p, err := readParams(r)
	if err != nil {
		s.badRequest(w, "bad_request", "could not read request body")
		return
	}
	amount, ok, err := p.int64("amount")
	if !ok {
		s.badRequest(w, "invalid_charge", "amount is required")
		return
	}
	if err != nil || amount < 0 {
		s.badRequest(w, "invalid_amount", "amount must be a non-negative integer")
		return
	}
	rawCurrency, ok := p.str("currency")
	currency := strings.ToUpper(strings.TrimSpace(rawCurrency))
	if !ok || len(currency) != 3 {
		s.badRequest(w, "invalid_charge", "currency is required")
		return
	}
	capture, err := p.bool("capture", true)
	if err != nil {
		s.badRequest(w, "invalid_charge", "capture must be true or false")
		return
	}
	description, _ := p.str("description")
	returnURI, _ := p.str("return_uri")
	ip, _ := p.str("ip")

	card, customer, aerr := s.resolveInstrument(p)
	if aerr != nil {
		writeError(w, aerr.status, aerr.body)
		return
	}

	c := &omise.Charge{
		ID:        omise.NewID("chrg"),
		Object:    "charge",
		Livemode:  false,
		Amount:    amount,
		Currency:  currency,
		Capture:   capture,
		Metadata:  p.metadata(),
		CreatedAt: omise.NewTimestamp(time.Now()),
		Card:      card,
	}
	c.Location = "/charges/" + c.ID
	if description != "" {
		c.Description = &description
	}
	if returnURI != "" {
		c.ReturnURI = &returnURI
	}
	if ip != "" {
		c.IP = &ip
	}
	if customer != nil {
		c.CustomerID = &customer.ID
	}

	required, frictionless := s.threeDSRequired(card, amount, r)

	switch {
	case card.DeclineCode != "":
		err = s.failCharge(c, card.DeclineCode, declineMessage(card.DeclineCode))
	case required && card.ThreeDSBehavior == string(omise.ThreeDSEnrollmentFail):
		err = s.failCharge(c, "payment_rejected", "3DS enrollment failed")
	case required && returnURI == "":
		err = s.failCharge(c, "payment_rejected", "3DS enrollment failed: return_uri is required")
	case required:
		err = s.startAuthorization(c, r)
	default:
		err = s.settleCharge(c, frictionless)
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.renderCharge(c))
}

// resolveInstrument works out which card a charge is aimed at, honouring Omise's
// rules: a token cannot be combined with a customer, a card must belong to the
// customer it is charged with, and a bare customer is charged on its default card.
func (s *Server) resolveInstrument(p *params) (*omise.Card, *omise.Customer, *apiError) {
	cardParam, _ := p.str("card")
	customerParam, _ := p.str("customer")

	switch {
	case strings.HasPrefix(cardParam, "tokn_"):
		if customerParam != "" {
			return nil, nil, bad("invalid_charge", "customer must not be present when charging a token")
		}
		tok, err := s.Store.ConsumeToken(cardParam)
		switch {
		case errIs(err, store.ErrUsedToken):
			return nil, nil, bad("used_token", "token was already used")
		case errIs(err, store.ErrNotFound):
			return nil, nil, missing()
		case err != nil:
			return nil, nil, &apiError{status: http.StatusInternalServerError, body: omise.NewError("internal_error", err.Error())}
		}
		return tok.Card, nil, nil

	case strings.HasPrefix(cardParam, "card_"):
		if customerParam == "" {
			return nil, nil, bad("invalid_charge", "a card identifier must be used together with its customer")
		}
		customer, err := s.Store.GetCustomer(customerParam)
		if err != nil || customer.Deleted {
			return nil, nil, missing()
		}
		card, err := s.Store.GetCard(cardParam)
		if err != nil || card.CustomerID != customer.ID || card.Deleted {
			return nil, nil, missing()
		}
		return card, customer, nil

	case cardParam != "":
		return nil, nil, bad("invalid_charge", "card must be a token or card identifier")

	case customerParam != "":
		customer, err := s.Store.GetCustomer(customerParam)
		if err != nil || customer.Deleted {
			return nil, nil, missing()
		}
		if customer.DefaultCard == nil {
			return nil, nil, bad("invalid_charge", "customer has no default card")
		}
		card, err := s.Store.GetCard(*customer.DefaultCard)
		if err != nil || card.Deleted {
			return nil, nil, bad("invalid_charge", "customer has no usable card")
		}
		return card, customer, nil

	default:
		return nil, nil, bad("invalid_charge", "charge must contain a card or a customer")
	}
}

// threeDSRequired decides whether this charge needs a challenge. The card's
// fingerprint, not the charge, is the unit of authentication (see docs/adr/0001),
// and a test can override the decision per request with X-Fake-3DS.
func (s *Server) threeDSRequired(card *omise.Card, amount int64, r *http.Request) (required bool, frictionless bool) {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("X-Fake-3DS"))) {
	case "off":
		return false, false
	case "challenge", "on":
		return true, false
	case "frictionless":
		return false, true
	}
	if card.ThreeDSBehavior == string(omise.ThreeDSNotEnrolled) {
		return false, false
	}
	switch s.Config.ThreeDS {
	case ThreeDSOff:
		return false, false
	case ThreeDSZeroAmount:
		if amount != 0 {
			return false, false
		}
	}
	authenticated, err := s.Store.IsFingerprintAuthenticated(card.Fingerprint)
	if err != nil {
		s.Config.Logger.Printf("fingerprint lookup: %v", err)
	}
	return !authenticated, false
}

func (s *Server) failCharge(c *omise.Charge, code, message string) error {
	c.Status = omise.StatusFailed
	c.FailureCode = &code
	c.FailureMessage = &message
	c.Authorized = false
	c.Paid = false
	c.Capturable = false
	if err := s.Store.CreateCharge(c); err != nil {
		return err
	}
	s.emit("charge.create", s.renderCharge(c))
	s.emit("charge.complete", s.renderCharge(c))
	return nil
}

// startAuthorization parks a charge in pending until the cardholder authenticates.
func (s *Server) startAuthorization(c *omise.Charge, r *http.Request) error {
	c.Status = omise.StatusPending
	c.Requires3DS = true
	c.PaymentID = omise.NewID("paym")
	uri := s.origin(r) + "/payments/" + c.PaymentID + "/authorize"
	c.AuthorizeURI = &uri
	expires := omise.NewTimestamp(time.Now().Add(time.Hour))
	c.ExpiresAt = &expires
	if err := s.Store.CreateCharge(c); err != nil {
		return err
	}
	s.emit("charge.create", s.renderCharge(c))
	return nil
}

// settleCharge authorizes (and optionally captures) a charge without a challenge.
func (s *Server) settleCharge(c *omise.Charge, frictionless bool) error {
	now := omise.NewTimestamp(time.Now())
	c.Status = omise.StatusSuccessful
	c.Authorized = true
	c.AuthorizedAt = &now
	c.AuthorizedAmount = c.Amount
	approval := approvalCode()
	c.ApprovalCode = &approval
	txn := omise.NewID("trxn")
	c.Transaction = &txn
	if c.Capture {
		c.Paid = true
		c.PaidAt = &now
		c.CapturedAmount = c.Amount
		c.Capturable = false
	} else {
		c.Capturable = true
	}
	if frictionless && c.Card != nil {
		if err := s.Store.AuthenticateFingerprint(c.Card.Fingerprint, c.ID); err != nil {
			return err
		}
	}
	if err := s.Store.CreateCharge(c); err != nil {
		return err
	}
	s.emit("charge.create", s.renderCharge(c))
	s.emit("charge.complete", s.renderCharge(c))
	return nil
}

// getCharge implements GET /charges/{id}.
func (s *Server) getCharge(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.GetCharge(r.PathValue("id"))
	if errIs(err, store.ErrNotFound) {
		s.notFound(w)
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.renderCharge(c))
}

// listCharges implements GET /charges.
func (s *Server) listCharges(w http.ResponseWriter, r *http.Request) {
	charges, err := s.Store.ListCharges()
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.renderCharges(charges, readPage(r)))
}

// captureCharge implements POST /charges/{id}/capture.
func (s *Server) captureCharge(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.GetCharge(r.PathValue("id"))
	if errIs(err, store.ErrNotFound) {
		s.notFound(w)
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	if c.Status == omise.StatusPending && c.ExpiresAt != nil && time.Now().After(c.ExpiresAt.Time) {
		s.badRequest(w, "expired_charge", "charge expired")
		return
	}
	if !c.Authorized {
		s.badRequest(w, "failed_capture", "charge is not authorized")
		return
	}
	if !c.Capturable {
		s.badRequest(w, "failed_capture", "capture failed: charge has already been captured")
		return
	}

	captureAmount := c.AuthorizedAmount
	if p, perr := readParams(r); perr == nil {
		if v, ok, _ := p.int64("capture_amount"); ok {
			if v <= 0 || v > c.AuthorizedAmount {
				s.badRequest(w, "failed_capture", "capture_amount must be between 1 and the authorized amount")
				return
			}
			captureAmount = v
		}
	}

	now := omise.NewTimestamp(time.Now())
	c.Paid = true
	c.PaidAt = &now
	c.CapturedAmount = captureAmount
	// A single capture releases whatever it did not capture; only multi-capture
	// charges can be captured again.
	c.Capturable = false
	c.Status = omise.StatusSuccessful
	if err := s.Store.UpdateCharge(c); err != nil {
		s.internal(w, err)
		return
	}
	rendered := s.renderCharge(c)
	s.emit("charge.capture", rendered)
	s.emit("charge.complete", rendered)
	writeJSON(w, http.StatusOK, rendered)
}

func approvalCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "000000"
	}
	s := n.String()
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}

func declineMessage(code string) string {
	switch code {
	case "insufficient_fund":
		return "insufficient funds in the account"
	case "stolen_or_lost_card":
		return "card was reported stolen or lost"
	case "failed_processing":
		return "general payment processing failure"
	case "payment_rejected":
		return "payment rejected by issuer"
	case "failed_fraud_check":
		return "card was marked as fraudulent"
	case "invalid_account_number":
		return "valid account for payment method not found"
	default:
		return "charge failed"
	}
}
