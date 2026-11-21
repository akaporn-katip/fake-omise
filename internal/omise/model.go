package omise

import "encoding/json"

// Charge statuses.
const (
	StatusPending    = "pending"
	StatusSuccessful = "successful"
	StatusFailed     = "failed"
	StatusExpired    = "expired"
)

// List is Omise's pagination envelope.
type List struct {
	Object   string    `json:"object"`
	Data     any       `json:"data"`
	Limit    int       `json:"limit"`
	Offset   int       `json:"offset"`
	Total    int       `json:"total"`
	Location string    `json:"location"`
	Order    string    `json:"order"`
	From     Timestamp `json:"from"`
	To       Timestamp `json:"to"`
}

// Card is a stored payment instrument.
type Card struct {
	Object             string    `json:"object"`
	ID                 string    `json:"id"`
	Livemode           bool      `json:"livemode"`
	Location           *string   `json:"location"`
	Deleted            bool      `json:"deleted"`
	Street1            *string   `json:"street1"`
	Street2            *string   `json:"street2"`
	City               *string   `json:"city"`
	State              *string   `json:"state"`
	PostalCode         *string   `json:"postal_code"`
	Country            *string   `json:"country"`
	PhoneNumber        *string   `json:"phone_number"`
	Financing          string    `json:"financing"`
	Bank               string    `json:"bank"`
	Brand              string    `json:"brand"`
	Fingerprint        string    `json:"fingerprint"`
	FirstDigits        *string   `json:"first_digits"`
	LastDigits         string    `json:"last_digits"`
	Name               string    `json:"name"`
	ExpirationMonth    int       `json:"expiration_month"`
	ExpirationYear     int       `json:"expiration_year"`
	SecurityCodeCheck  bool      `json:"security_code_check"`
	TokenizationMethod *string   `json:"tokenization_method"`
	CreatedAt          Timestamp `json:"created_at"`
	// ThreeDSAuthenticatedAt is a deliberate extension: Omise has no such field,
	// but it makes "why did this charge skip 3DS?" answerable. See docs/adr/0001.
	ThreeDSAuthenticatedAt *Timestamp `json:"three_ds_authenticated_at"`

	// Internal, never serialised.
	CustomerID      string `json:"-"`
	TokenID         string `json:"-"`
	ThreeDSBehavior string `json:"-"`
	DeclineCode     string `json:"-"`
}

// Token is a single-use representation of card details.
type Token struct {
	Object    string    `json:"object"`
	ID        string    `json:"id"`
	Livemode  bool      `json:"livemode"`
	Location  string    `json:"location"`
	Used      bool      `json:"used"`
	Card      *Card     `json:"card"`
	CreatedAt Timestamp `json:"created_at"`
}

// Customer owns cards so they can be charged again.
type Customer struct {
	Object      string         `json:"object"`
	ID          string         `json:"id"`
	Livemode    bool           `json:"livemode"`
	Location    string         `json:"location"`
	Email       string         `json:"email"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata"`
	DefaultCard *string        `json:"default_card"`
	Cards       List           `json:"cards"`
	Deleted     bool           `json:"deleted"`
	CreatedAt   Timestamp      `json:"created_at"`
}

// Charge is an attempt to move money from a card to the merchant.
type Charge struct {
	Object                   string         `json:"object"`
	ID                       string         `json:"id"`
	Livemode                 bool           `json:"livemode"`
	Location                 string         `json:"location"`
	Amount                   int64          `json:"amount"`
	Net                      int64          `json:"net"`
	Fee                      int64          `json:"fee"`
	FeeVAT                   int64          `json:"fee_vat"`
	Interest                 int64          `json:"interest"`
	InterestVAT              int64          `json:"interest_vat"`
	FundingAmount            int64          `json:"funding_amount"`
	RefundedAmount           int64          `json:"refunded_amount"`
	Currency                 string         `json:"currency"`
	FundingCurrency          string         `json:"funding_currency"`
	IP                       *string        `json:"ip"`
	Refunds                  List           `json:"refunds"`
	Link                     *string        `json:"link"`
	Description              *string        `json:"description"`
	Metadata                 map[string]any `json:"metadata"`
	Card                     *Card          `json:"card"`
	Source                   any            `json:"source"`
	Schedule                 any            `json:"schedule"`
	CustomerID               *string        `json:"customer"`
	Dispute                  any            `json:"dispute"`
	Transaction              *string        `json:"transaction"`
	FailureCode              *string        `json:"failure_code"`
	FailureMessage           *string        `json:"failure_message"`
	Status                   string         `json:"status"`
	AuthorizeURI             *string        `json:"authorize_uri"`
	ReturnURI                *string        `json:"return_uri"`
	AuthenticatedBy          *string        `json:"authenticated_by"`
	AuthorizedAmount         int64          `json:"authorized_amount"`
	CapturedAmount           int64          `json:"captured_amount"`
	ApprovalCode             *string        `json:"approval_code"`
	AuthorizedAt             *Timestamp     `json:"authorized_at"`
	CreatedAt                Timestamp      `json:"created_at"`
	PaidAt                   *Timestamp     `json:"paid_at"`
	ExpiresAt                *Timestamp     `json:"expires_at"`
	ExpiredAt                *Timestamp     `json:"expired_at"`
	ReversedAt               *Timestamp     `json:"reversed_at"`
	ZeroInterestInstallments bool           `json:"zero_interest_installments"`

	Authorized bool `json:"authorized"`
	Capturable bool `json:"capturable"`
	Capture    bool `json:"capture"`
	Disputable bool `json:"disputable"`
	Refundable bool `json:"refundable"`
	Reversed   bool `json:"reversed"`
	Reversible bool `json:"reversible"`
	Voided     bool `json:"voided"`
	Paid       bool `json:"paid"`
	Expired    bool `json:"expired"`

	// Internal, never serialised.
	CardID      string `json:"-"`
	PaymentID   string `json:"-"`
	Requires3DS bool   `json:"-"`
}

// Event is something worth telling webhooks about.
type Event struct {
	Object            string            `json:"object"`
	ID                string            `json:"id"`
	Livemode          bool              `json:"livemode"`
	Location          string            `json:"location"`
	Key               string            `json:"key"`
	CreatedAt         Timestamp         `json:"created_at"`
	Data              any               `json:"data"`
	WebhookDeliveries []WebhookDelivery `json:"webhook_deliveries"`
}

// WebhookDelivery records one attempt to POST an event to a merchant endpoint.
type WebhookDelivery struct {
	Object    string    `json:"object"`
	ID        string    `json:"id"`
	URI       string    `json:"uri"`
	Status    int       `json:"status"`
	Key       string    `json:"key"`
	Error     string    `json:"error"`
	Payload   string    `json:"payload"`
	CreatedAt Timestamp `json:"created_at"`
}

// Error is Omise's error envelope.
type Error struct {
	Object   string `json:"object"`
	Location string `json:"location"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func NewError(code, message string) *Error {
	return &Error{
		Object:   "error",
		Location: "https://docs.omise.co/api/errors#" + code,
		Code:     code,
		Message:  message,
	}
}

// Metadata decodes a stored metadata blob, always returning a non-nil map.
func Metadata(raw string) map[string]any {
	m := map[string]any{}
	if raw == "" {
		return m
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{}
	}
	return m
}

// EncodeMetadata marshals metadata, tolerating nil.
func EncodeMetadata(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}
