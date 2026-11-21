package omise

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// NewID returns an identifier shaped like Omise's: <prefix>_test_<24 chars>.
func NewID(prefix string) string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return prefix + "_test_" + string(b)
}

// Timestamp serialises as Omise does: UTC, second precision, e.g. 2019-12-31T12:59:59Z.
type Timestamp struct{ time.Time }

func NewTimestamp(t time.Time) Timestamp { return Timestamp{t.UTC().Truncate(time.Second)} }

func (t Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(`"` + t.UTC().Format("2006-01-02T15:04:05Z") + `"`), nil
}

func (t *Timestamp) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	t.Time = parsed.UTC()
	return nil
}

// ParseTimestamp reads a timestamp produced by Timestamp.MarshalJSON.
func ParseTimestamp(s string) Timestamp {
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return Timestamp{}
	}
	return Timestamp{parsed.UTC()}
}

// Fingerprint identifies the payment instrument behind a card number. Two cards
// created from the same number share a fingerprint and therefore share 3DS
// authentication state (see docs/adr/0003).
func Fingerprint(number string) string {
	sum := sha256.Sum256([]byte(normalizeNumber(number)))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func normalizeNumber(number string) string {
	var b strings.Builder
	for _, r := range number {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Digits strips every non-digit from a card number.
func Digits(number string) string { return normalizeNumber(number) }

// LastDigits returns the last four digits of a card number.
func LastDigits(number string) string {
	d := normalizeNumber(number)
	if len(d) <= 4 {
		return d
	}
	return d[len(d)-4:]
}

// Brand guesses the card brand from its number, the way Omise reports it.
func Brand(number string) string {
	d := normalizeNumber(number)
	switch {
	case strings.HasPrefix(d, "4"):
		return "Visa"
	case strings.HasPrefix(d, "34"), strings.HasPrefix(d, "37"):
		return "Amex"
	case strings.HasPrefix(d, "35"):
		return "JCB"
	case strings.HasPrefix(d, "36"), strings.HasPrefix(d, "38"), strings.HasPrefix(d, "30"):
		return "Diners"
	case strings.HasPrefix(d, "6011"), strings.HasPrefix(d, "6447"), strings.HasPrefix(d, "65"):
		return "Discover"
	case strings.HasPrefix(d, "62"):
		return "UnionPay"
	case hasMastercardPrefix(d):
		return "Mastercard"
	default:
		return "Unknown"
	}
}

func hasMastercardPrefix(d string) bool {
	if len(d) < 2 {
		return false
	}
	if d[0] == '5' && d[1] >= '1' && d[1] <= '5' {
		return true
	}
	if len(d) >= 4 {
		n := 0
		for _, r := range d[:4] {
			n = n*10 + int(r-'0')
		}
		return n >= 2221 && n <= 2720
	}
	return false
}

// ThreeDSBehavior is how a test card behaves during a 3-D Secure challenge.
type ThreeDSBehavior string

const (
	ThreeDSChallenge      ThreeDSBehavior = "challenge"
	ThreeDSValidationFail ThreeDSBehavior = "validation_fail"
	ThreeDSEnrollmentFail ThreeDSBehavior = "enrollment_fail"
	ThreeDSNotEnrolled    ThreeDSBehavior = "none"
)

// TestCard mirrors Omise's documented test cards: https://docs.omise.co/api-testing
type TestCard struct {
	Number      string
	Brand       string
	DeclineCode string // failure_code the charge fails with at creation, if any
	ThreeDS     ThreeDSBehavior
}

var testCards = []TestCard{
	// Cards that succeed.
	{Number: "4242424242424242", Brand: "Visa", ThreeDS: ThreeDSChallenge},
	{Number: "4111111111111111", Brand: "Visa", ThreeDS: ThreeDSChallenge},
	{Number: "5555555555554444", Brand: "Mastercard", ThreeDS: ThreeDSChallenge},
	{Number: "5454545454545454", Brand: "Mastercard", ThreeDS: ThreeDSChallenge},
	{Number: "3530111333300000", Brand: "JCB", ThreeDS: ThreeDSChallenge},
	{Number: "3566111111111113", Brand: "JCB", ThreeDS: ThreeDSChallenge},
	{Number: "378282246310005", Brand: "Amex", ThreeDS: ThreeDSChallenge},
	{Number: "6447032400000000001", Brand: "Discover", ThreeDS: ThreeDSChallenge},
	{Number: "36021042000004", Brand: "Diners", ThreeDS: ThreeDSChallenge},
	{Number: "6250947000000006", Brand: "UnionPay", ThreeDS: ThreeDSChallenge},

	// Cards that fail at creation with a specific failure_code.
	{Number: "4111111111140011", Brand: "Visa", DeclineCode: "insufficient_fund"},
	{Number: "5555551111110011", Brand: "Mastercard", DeclineCode: "insufficient_fund"},
	{Number: "3530111111190011", Brand: "JCB", DeclineCode: "insufficient_fund"},
	{Number: "6250947000000014", Brand: "UnionPay", DeclineCode: "insufficient_fund"},
	{Number: "4111111111130012", Brand: "Visa", DeclineCode: "stolen_or_lost_card"},
	{Number: "5555551111100012", Brand: "Mastercard", DeclineCode: "stolen_or_lost_card"},
	{Number: "3530111111180012", Brand: "JCB", DeclineCode: "stolen_or_lost_card"},
	{Number: "6250947000000022", Brand: "UnionPay", DeclineCode: "stolen_or_lost_card"},
	{Number: "4111111111120013", Brand: "Visa", DeclineCode: "failed_processing"},
	{Number: "5555551111190013", Brand: "Mastercard", DeclineCode: "failed_processing"},
	{Number: "3530111111170013", Brand: "JCB", DeclineCode: "failed_processing"},
	{Number: "377138160000009", Brand: "Amex", DeclineCode: "failed_processing"},
	{Number: "6447032400001111005", Brand: "Discover", DeclineCode: "failed_processing"},
	{Number: "36021042111108", Brand: "Diners", DeclineCode: "failed_processing"},
	{Number: "6250947000000030", Brand: "UnionPay", DeclineCode: "failed_processing"},
	{Number: "4111111111110014", Brand: "Visa", DeclineCode: "payment_rejected"},
	{Number: "5555551111180014", Brand: "Mastercard", DeclineCode: "payment_rejected"},
	{Number: "3530111111160014", Brand: "JCB", DeclineCode: "payment_rejected"},
	{Number: "6250947000000048", Brand: "UnionPay", DeclineCode: "payment_rejected"},
	{Number: "4111111111190016", Brand: "Visa", DeclineCode: "failed_fraud_check"},
	{Number: "5555551111160016", Brand: "Mastercard", DeclineCode: "failed_fraud_check"},
	{Number: "3530111111140016", Brand: "JCB", DeclineCode: "failed_fraud_check"},
	{Number: "6250947000000055", Brand: "UnionPay", DeclineCode: "failed_fraud_check"},
	{Number: "4111111111180017", Brand: "Visa", DeclineCode: "invalid_account_number"},
	{Number: "5555551111150017", Brand: "Mastercard", DeclineCode: "invalid_account_number"},
	{Number: "3530111111130017", Brand: "JCB", DeclineCode: "invalid_account_number"},
	{Number: "6250947000000063", Brand: "UnionPay", DeclineCode: "invalid_account_number"},

	// Cards that fail the 3DS challenge itself.
	{Number: "4111111111150002", Brand: "Visa", ThreeDS: ThreeDSEnrollmentFail},
	{Number: "5555551111120002", Brand: "Mastercard", ThreeDS: ThreeDSEnrollmentFail},
	{Number: "3530111111100002", Brand: "JCB", ThreeDS: ThreeDSEnrollmentFail},
	{Number: "6250947000000071", Brand: "UnionPay", ThreeDS: ThreeDSEnrollmentFail},
	{Number: "4111111111140003", Brand: "Visa", ThreeDS: ThreeDSValidationFail},
	{Number: "5555551111110003", Brand: "Mastercard", ThreeDS: ThreeDSValidationFail},
	{Number: "3530111111190003", Brand: "JCB", ThreeDS: ThreeDSValidationFail},
	{Number: "377138161111003", Brand: "Amex", ThreeDS: ThreeDSValidationFail},
	{Number: "6447032400002222009", Brand: "Discover", ThreeDS: ThreeDSValidationFail},
	{Number: "36021042222202", Brand: "Diners", ThreeDS: ThreeDSValidationFail},
	{Number: "6250947000000089", Brand: "UnionPay", ThreeDS: ThreeDSValidationFail},
}

var testCardIndex = func() map[string]TestCard {
	m := make(map[string]TestCard, len(testCards))
	for _, c := range testCards {
		m[c.Number] = c
	}
	return m
}()

// LookupTestCard returns the documented behaviour for a card number. Numbers
// that are not in the table behave like a plain successful card.
func LookupTestCard(number string) TestCard {
	d := normalizeNumber(number)
	if c, ok := testCardIndex[d]; ok {
		return c
	}
	return TestCard{Number: d, Brand: Brand(d), ThreeDS: ThreeDSChallenge}
}
