package api

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/akaporn-katip/fake-omise/internal/omise"
	"github.com/akaporn-katip/fake-omise/internal/store"
)

type authorizeView struct {
	ChargeID   string
	Amount     string
	LastDigits string
	Brand      string
	ReturnURI  string
	Status     string
	Done       bool
	Failed     bool
	Message    string
}

var authorizeTmpl = template.Must(template.New("authorize").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>3-D Secure — fake-omise</title>
<style>
  :root { color-scheme: light dark; --card:#fff; --ink:#111; --muted:#666; --line:#e3e3e3; --brand:#0a5cff; }
  @media (prefers-color-scheme: dark) { :root { --card:#16181d; --ink:#f2f2f2; --muted:#9aa0a6; --line:#2b2f36; } }
  * { box-sizing: border-box; }
  body { margin:0; min-height:100vh; display:grid; place-items:center; background:#f4f5f7;
         font:15px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; color:var(--ink); padding:24px; }
  @media (prefers-color-scheme: dark) { body { background:#0e1013; } }
  .card { background:var(--card); width:100%; max-width:420px; border:1px solid var(--line);
          border-radius:16px; padding:28px; box-shadow:0 18px 40px rgba(0,0,0,.08); }
  .eyebrow { font-size:12px; letter-spacing:.08em; text-transform:uppercase; color:var(--muted); }
  h1 { font-size:20px; margin:6px 0 18px; }
  .amount { font-size:34px; font-weight:650; letter-spacing:-.02em; }
  .summary { display:flex; justify-content:space-between; align-items:baseline; margin:16px 0 20px;
             padding-top:16px; border-top:1px solid var(--line); font-size:14px; color:var(--muted); }
  label { display:block; font-size:13px; color:var(--muted); margin-bottom:6px; }
  input { width:100%; padding:12px 14px; font-size:17px; letter-spacing:.3em; border-radius:10px;
          border:1px solid var(--line); background:transparent; color:inherit; }
  input:focus { outline:2px solid var(--brand); outline-offset:1px; }
  .row { display:flex; gap:10px; margin-top:18px; }
  button { flex:1; padding:12px 14px; border-radius:10px; border:1px solid var(--line); font-size:15px;
           font-weight:600; cursor:pointer; background:transparent; color:inherit; }
  button.primary { background:var(--brand); border-color:var(--brand); color:#fff; }
  .hint { margin-top:18px; font-size:12px; color:var(--muted); }
  .badge { display:inline-block; padding:4px 10px; border-radius:999px; font-size:13px; font-weight:600; }
  .badge.ok { background:#e6f6ec; color:#116a3a; }
  .badge.fail { background:#fdeaea; color:#a11616; }
  @media (prefers-color-scheme: dark) { .badge.ok { background:#123522; color:#7ee2a8; }
                                        .badge.fail { background:#3a1515; color:#ff9d9d; } }
</style>
</head>
<body>
  <main class="card">
    <div class="eyebrow">3-D Secure verification</div>
    <h1>{{if .Done}}Result for {{.ChargeID}}{{else}}Confirm your payment{{end}}</h1>

    <div class="amount">{{.Amount}}</div>
    <div class="summary">
      <span>{{.Brand}} •••• {{.LastDigits}}</span>
      <span>{{.ChargeID}}</span>
    </div>

    {{if .Done}}
      {{if .Failed}}
        <span class="badge fail">Authentication failed</span>
        <p class="hint">{{.Message}}</p>
      {{else}}
        <span class="badge ok">Authenticated</span>
        <p class="hint">{{.Message}}</p>
      {{end}}
      {{if .ReturnURI}}<p class="hint">You can return to <a href="{{.ReturnURI}}">{{.ReturnURI}}</a>.</p>{{end}}
    {{else}}
      <form method="post" action="">
        <label for="otp">One-time password</label>
        <input id="otp" name="otp" inputmode="numeric" autocomplete="one-time-code"
               placeholder="123456" maxlength="8" autofocus>
        <div class="row">
          <button type="submit" name="outcome" value="fail">Decline</button>
          <button type="submit" name="outcome" value="success" class="primary">Authenticate</button>
        </div>
      </form>
      <p class="hint">Any one-time password is accepted by fake-omise. Test cards that fail
      authentication are rejected regardless of what you enter.</p>
    {{end}}
  </main>
</body>
</html>`))

// authorizationPage implements GET /payments/{id}/authorize.
func (s *Server) authorizationPage(w http.ResponseWriter, r *http.Request) {
	c, oerr := s.chargeForPayment(r.PathValue("id"))
	if oerr != nil {
		writeError(w, oerr.status, oerr.body)
		return
	}
	card, _ := s.Store.GetCard(c.CardID)

	view := authorizeView{
		ChargeID:   c.ID,
		Amount:     formatAmount(c.Amount, c.Currency),
		LastDigits: "0000",
		Brand:      "Card",
		ReturnURI:  valueOr(c.ReturnURI, ""),
	}
	if card != nil {
		view.LastDigits = card.LastDigits
		view.Brand = card.Brand
	}

	switch c.Status {
	case omise.StatusPending:
	case omise.StatusFailed:
		view.Done = true
		view.Failed = true
		view.Message = valueOr(c.FailureMessage, "The payment was not authenticated.")
	default:
		view.Done = true
		view.Message = "This payment has already been completed."
	}
	writeHTML(w, authorizeTmpl, view)
}

// submitAuthorization implements POST /payments/{id}/authorize.
func (s *Server) submitAuthorization(w http.ResponseWriter, r *http.Request) {
	c, oerr := s.chargeForPayment(r.PathValue("id"))
	if oerr != nil {
		writeError(w, oerr.status, oerr.body)
		return
	}
	if c.Status != omise.StatusPending {
		http.Redirect(w, r, valueOr(c.ReturnURI, "/payments/"+c.PaymentID+"/authorize"), http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "bad_request", "could not read form")
		return
	}
	success := r.PostForm.Get("outcome") != "fail"
	if err := s.completeAuthorization(c, success); err != nil {
		s.internal(w, err)
		return
	}
	if uri := valueOr(c.ReturnURI, ""); uri != "" {
		http.Redirect(w, r, uri, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/payments/"+c.PaymentID+"/authorize", http.StatusSeeOther)
}

// completeAuthorization applies the outcome of a challenge exactly once.
func (s *Server) completeAuthorization(c *omise.Charge, success bool) error {
	if c.Status != omise.StatusPending {
		return nil
	}
	card, err := s.Store.GetCard(c.CardID)
	if err != nil {
		return err
	}
	now := omise.NewTimestamp(time.Now())

	// Some test cards always fail the challenge, no matter what the cardholder enters.
	if !success || card.ThreeDSBehavior == string(omise.ThreeDSValidationFail) {
		code := "payment_rejected"
		message := "3DS authentication failed"
		if !success {
			message = "3DS authentication was declined by the cardholder"
		}
		c.Status = omise.StatusFailed
		c.FailureCode = &code
		c.FailureMessage = &message
		c.Authorized = false
		c.Paid = false
		c.Capturable = false
		if err := s.Store.UpdateCharge(c); err != nil {
			return err
		}
		s.emit("charge.complete", s.renderCharge(c))
		return nil
	}

	c.Status = omise.StatusSuccessful
	c.Authorized = true
	c.AuthorizedAt = &now
	c.AuthorizedAmount = c.Amount
	authenticatedBy := "3DS"
	c.AuthenticatedBy = &authenticatedBy
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
	if err := s.Store.UpdateCharge(c); err != nil {
		return err
	}
	if err := s.Store.AuthenticateFingerprint(card.Fingerprint, c.ID); err != nil {
		return err
	}
	s.emit("charge.complete", s.renderCharge(c))
	return nil
}

func (s *Server) chargeForPayment(paymentID string) (*omise.Charge, *apiError) {
	c, err := s.Store.GetChargeByPaymentID(paymentID)
	if errIs(err, store.ErrNotFound) {
		return nil, missing()
	}
	if err != nil {
		return nil, &apiError{status: http.StatusInternalServerError, body: omise.NewError("internal_error", err.Error())}
	}
	return c, nil
}

func writeHTML(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = tmpl.Execute(w, data)
}

// formatAmount renders satang as a human-readable amount.
func formatAmount(amount int64, currency string) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}
	unit := amount / 100
	frac := amount % 100
	symbol := currencySymbol(currency)
	return fmt.Sprintf("%s%s%s.%02d %s", sign, symbol, withThousands(unit), frac, strings.ToUpper(currency))
}

func withThousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	var out []byte
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(r))
	}
	return string(out)
}

func currencySymbol(currency string) string {
	switch strings.ToUpper(currency) {
	case "THB":
		return "฿"
	case "JPY":
		return "¥"
	case "USD":
		return "$"
	case "SGD":
		return "S$"
	default:
		return ""
	}
}
