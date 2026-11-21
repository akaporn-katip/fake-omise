package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akaporn-katip/fake-omise/internal/fakeomise"
)

const testKey = "skey_test_4xs8breq3htbkj03d2x"

type client struct {
	t    *testing.T
	base string
	hc   *http.Client
}

func newClient(t *testing.T, base string) *client {
	t.Helper()
	return &client{t: t, base: base, hc: &http.Client{Timeout: 5 * time.Second}}
}

func (c *client) rawDo(method, path string, form url.Values) ([]byte, int, http.Header) {
	c.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	req.SetBasicAuth(testKey, "")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read body: %v", err)
	}
	return raw, resp.StatusCode, resp.Header
}

func (c *client) do(method, path string, form url.Values) (map[string]any, int, http.Header) {
	c.t.Helper()
	raw, status, header := c.rawDo(method, path, form)
	out := map[string]any{}
	if strings.Contains(header.Get("Content-Type"), "application/json") && len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return out, status, header
}

func (c *client) post(path string, form url.Values) (map[string]any, int) {
	c.t.Helper()
	obj, status, _ := c.do(http.MethodPost, path, form)
	return obj, status
}

func (c *client) get(path string) (map[string]any, int) {
	c.t.Helper()
	obj, status, _ := c.do(http.MethodGet, path, nil)
	return obj, status
}

func str(t *testing.T, obj map[string]any, key string) string {
	t.Helper()
	v, ok := obj[key].(string)
	if !ok {
		t.Fatalf("field %q is not a string in %v", key, obj)
	}
	return v
}

func boolean(t *testing.T, obj map[string]any, key string) bool {
	t.Helper()
	v, ok := obj[key].(bool)
	if !ok {
		t.Fatalf("field %q is not a bool in %v", key, obj)
	}
	return v
}

func number(t *testing.T, obj map[string]any, key string) int64 {
	t.Helper()
	v, ok := obj[key].(float64)
	if !ok {
		t.Fatalf("field %q is not a number in %v", key, obj)
	}
	return int64(v)
}

// createToken tokenizes a card number.
func (c *client) createToken(number string) map[string]any {
	c.t.Helper()
	form := url.Values{}
	form.Set("card[number]", number)
	form.Set("card[expiration_month]", "12")
	form.Set("card[expiration_year]", "2030")
	form.Set("card[security_code]", "123")
	form.Set("card[name]", "Somchai Prasert")
	obj, status := c.post("/tokens", form)
	if status != http.StatusOK {
		c.t.Fatalf("create token: status %d: %v", status, obj)
	}
	return obj
}

// createCustomer stores a card under a new customer and returns (customer, card).
func (c *client) createCustomer(tokenID string) (map[string]any, map[string]any) {
	c.t.Helper()
	form := url.Values{}
	form.Set("email", "somchai@example.com")
	form.Set("description", "coffee subscription")
	if tokenID != "" {
		form.Set("card", tokenID)
	}
	obj, status := c.post("/customers", form)
	if status != http.StatusOK {
		c.t.Fatalf("create customer: status %d: %v", status, obj)
	}
	card, _ := obj["default_card"].(string)
	cards := obj["cards"].(map[string]any)["data"].([]any)
	var cardObj map[string]any
	for _, raw := range cards {
		if m, ok := raw.(map[string]any); ok && m["id"] == card {
			cardObj = m
		}
	}
	return obj, cardObj
}

func (c *client) charge(form url.Values) (map[string]any, int) {
	c.t.Helper()
	return c.post("/charges", form)
}

// authorize finishes a pending charge's 3DS challenge headlessly.
func (c *client) authorize(chargeID, outcome string) map[string]any {
	c.t.Helper()
	obj, status := c.post("/_fake/charges/"+chargeID+"/authorize", url.Values{"outcome": {outcome}})
	if status != http.StatusOK {
		c.t.Fatalf("authorize: status %d: %v", status, obj)
	}
	return obj
}

// TestThreeDSOnceThenSilentRecurring walks the story this fake exists for: the
// first charge authenticates the card with 3DS, later charges do not.
func TestThreeDSOnceThenSilentRecurring(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{})
	c := newClient(t, srv.URL)

	tok := c.createToken("4242424242424242")
	customer, card := c.createCustomer(str(t, tok, "id"))
	customerID := str(t, customer, "id")
	cardID := str(t, card, "id")

	// First charge: 0 THB to validate and enrol the card, with 3DS.
	form := url.Values{}
	form.Set("amount", "0")
	form.Set("currency", "THB")
	form.Set("customer", customerID)
	form.Set("return_uri", "https://merchant.example/orders/1/complete")
	first, status := c.charge(form)
	if status != http.StatusOK {
		t.Fatalf("first charge: status %d: %v", status, first)
	}
	if got := str(t, first, "status"); got != "pending" {
		t.Fatalf("first charge status = %q, want pending", got)
	}
	authorizeURI := str(t, first, "authorize_uri")
	if !strings.Contains(authorizeURI, "/payments/") {
		t.Fatalf("authorize_uri = %q, want a /payments/ path", authorizeURI)
	}

	done := c.authorize(str(t, first, "id"), "success")
	if got := str(t, done, "status"); got != "successful" {
		t.Fatalf("after 3DS status = %q, want successful (%v)", got, done)
	}
	if got := str(t, done, "authenticated_by"); got != "3DS" {
		t.Fatalf("authenticated_by = %q, want 3DS", got)
	}
	if !boolean(t, done, "paid") {
		t.Fatal("0-amount validation charge should be paid")
	}
	authedCard, ok := done["card"].(map[string]any)
	if !ok || authedCard["three_ds_authenticated_at"] == nil {
		t.Fatalf("card should record when it was authenticated: %v", done["card"])
	}

	// Second charge: no challenge, no return_uri needed.
	form = url.Values{}
	form.Set("amount", "4900")
	form.Set("currency", "THB")
	form.Set("customer", customerID)
	second, status := c.charge(form)
	if status != http.StatusOK {
		t.Fatalf("second charge: status %d: %v", status, second)
	}
	if got := str(t, second, "status"); got != "successful" {
		t.Fatalf("second charge status = %q, want successful (%v)", got, second)
	}
	if second["authorize_uri"] != nil {
		t.Fatalf("second charge asked for 3DS again: %v", second["authorize_uri"])
	}
	if !boolean(t, second, "paid") {
		t.Fatal("second charge should be captured immediately")
	}
	if got := number(t, second, "amount"); got != 4900 {
		t.Fatalf("second charge amount = %d, want 4900", got)
	}
	if got := str(t, second, "customer"); got != customerID {
		t.Fatalf("second charge customer = %q, want %q", got, customerID)
	}
	if cardField, ok := second["card"].(map[string]any); !ok || str(t, cardField, "id") != cardID {
		t.Fatalf("second charge card = %v, want %s", second["card"], cardID)
	}
}

// TestAuthorizationPageRedirectsToReturnURI checks the browser flow, not just
// the headless shortcut.
func TestAuthorizationPageRedirectsToReturnURI(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{})
	c := newClient(t, srv.URL)

	tok := c.createToken("4242424242424242")
	customer, _ := c.createCustomer(str(t, tok, "id"))
	form := url.Values{}
	form.Set("amount", "10000")
	form.Set("currency", "THB")
	form.Set("customer", str(t, customer, "id"))
	form.Set("return_uri", "https://merchant.example/orders/42/complete")
	charge, _ := c.charge(form)
	authorizeURI := str(t, charge, "authorize_uri")

	page, status, _ := c.rawDo(http.MethodGet, strings.TrimPrefix(authorizeURI, srv.URL), nil)
	if !strings.Contains(string(page), "Confirm your payment") {
		t.Fatalf("authorization page did not render a challenge: %s", page)
	}
	if status != http.StatusOK {
		t.Fatalf("authorization page status = %d", status)
	}

	req, err := http.NewRequest(http.MethodPost, authorizeURI, strings.NewReader("outcome=success&otp=123456"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	noRedirect := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("submit authorization: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("submit status = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "https://merchant.example/orders/42/complete" {
		t.Fatalf("redirect = %q, want the return_uri", got)
	}

	final, _ := c.get("/charges/" + str(t, charge, "id"))
	if got := str(t, final, "status"); got != "successful" {
		t.Fatalf("charge status after page = %q, want successful", got)
	}
}

func TestTokenIsSingleUse(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{})
	c := newClient(t, srv.URL)

	tok := c.createToken("4242424242424242")
	form := url.Values{}
	form.Set("amount", "1000")
	form.Set("currency", "THB")
	form.Set("card", str(t, tok, "id"))

	if _, status := c.charge(form); status != http.StatusOK {
		t.Fatalf("first charge status = %d", status)
	}
	second, status := c.charge(form)
	if status != http.StatusBadRequest {
		t.Fatalf("second charge status = %d, want 400", status)
	}
	if got := str(t, second, "code"); got != "used_token" {
		t.Fatalf("second charge code = %q, want used_token", got)
	}
}

func TestDeclinedCardFailsAtCreation(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{})
	c := newClient(t, srv.URL)

	tok := c.createToken("4111111111140011")
	form := url.Values{}
	form.Set("amount", "5000")
	form.Set("currency", "THB")
	form.Set("card", str(t, tok, "id"))
	charge, status := c.charge(form)
	if status != http.StatusOK {
		t.Fatalf("charge status = %d: %v", status, charge)
	}
	if got := str(t, charge, "status"); got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	if got := str(t, charge, "failure_code"); got != "insufficient_fund" {
		t.Fatalf("failure_code = %q, want insufficient_fund", got)
	}
	if charge["authorize_uri"] != nil {
		t.Fatal("declined card should not be sent to 3DS")
	}
}

func TestThreeDSValidationFailureCard(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{})
	c := newClient(t, srv.URL)

	tok := c.createToken("4111111111140003")
	customer, _ := c.createCustomer(str(t, tok, "id"))
	form := url.Values{}
	form.Set("amount", "5000")
	form.Set("currency", "THB")
	form.Set("customer", str(t, customer, "id"))
	form.Set("return_uri", "https://merchant.example/complete")
	charge, _ := c.charge(form)
	if got := str(t, charge, "status"); got != "pending" {
		t.Fatalf("status = %q, want pending", got)
	}
	done := c.authorize(str(t, charge, "id"), "success")
	if got := str(t, done, "status"); got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	if got := str(t, done, "failure_code"); got != "payment_rejected" {
		t.Fatalf("failure_code = %q, want payment_rejected", got)
	}

	// The card must still require a challenge next time.
	form = url.Values{}
	form.Set("amount", "5000")
	form.Set("currency", "THB")
	form.Set("customer", str(t, customer, "id"))
	form.Set("return_uri", "https://merchant.example/complete")
	again, _ := c.charge(form)
	if got := str(t, again, "status"); got != "pending" {
		t.Fatalf("retry status = %q, want pending (card was never authenticated)", got)
	}
}

func TestMissingReturnURIFailsEnrolment(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{})
	c := newClient(t, srv.URL)

	tok := c.createToken("4242424242424242")
	customer, _ := c.createCustomer(str(t, tok, "id"))
	form := url.Values{}
	form.Set("amount", "1000")
	form.Set("currency", "THB")
	form.Set("customer", str(t, customer, "id"))
	charge, _ := c.charge(form)
	if got := str(t, charge, "status"); got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	if got := str(t, charge, "failure_code"); got != "payment_rejected" {
		t.Fatalf("failure_code = %q, want payment_rejected", got)
	}
}

func TestCaptureAfterAuthorizeOnly(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{})
	c := newClient(t, srv.URL)

	tok := c.createToken("4242424242424242")
	customer, _ := c.createCustomer(str(t, tok, "id"))
	form := url.Values{}
	form.Set("amount", "20000")
	form.Set("currency", "THB")
	form.Set("customer", str(t, customer, "id"))
	form.Set("return_uri", "https://merchant.example/complete")
	form.Set("capture", "false")
	charge, _ := c.charge(form)
	authorized := c.authorize(str(t, charge, "id"), "success")

	if boolean(t, authorized, "paid") {
		t.Fatal("capture=false charge should not be paid yet")
	}
	if !boolean(t, authorized, "capturable") {
		t.Fatal("capture=false charge should be capturable")
	}

	captured, status := c.post("/charges/"+str(t, charge, "id")+"/capture", url.Values{})
	if status != http.StatusOK {
		t.Fatalf("capture status = %d: %v", status, captured)
	}
	if !boolean(t, captured, "paid") {
		t.Fatal("captured charge should be paid")
	}
	if got := number(t, captured, "captured_amount"); got != 20000 {
		t.Fatalf("captured_amount = %d, want 20000", got)
	}

	if _, status := c.post("/charges/"+str(t, charge, "id")+"/capture", url.Values{}); status != http.StatusBadRequest {
		t.Fatalf("second capture status = %d, want 400", status)
	}
}

func TestPartialCaptureReleasesTheRemainder(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{ThreeDS: "off"})
	c := newClient(t, srv.URL)

	form := url.Values{}
	form.Set("amount", "20000")
	form.Set("currency", "THB")
	form.Set("card", str(t, c.createToken("4242424242424242"), "id"))
	form.Set("capture", "false")
	charge, _ := c.charge(form)
	if !boolean(t, charge, "capturable") {
		t.Fatalf("charge should start capturable: %v", charge)
	}

	partial, status := c.post("/charges/"+str(t, charge, "id")+"/capture", url.Values{"capture_amount": {"15000"}})
	if status != http.StatusOK {
		t.Fatalf("partial capture status = %d: %v", status, partial)
	}
	if got := number(t, partial, "captured_amount"); got != 15000 {
		t.Fatalf("captured_amount = %d, want 15000", got)
	}
	if boolean(t, partial, "capturable") {
		t.Fatal("a single partial capture must release the remainder")
	}
	again, status := c.post("/charges/"+str(t, charge, "id")+"/capture", url.Values{"capture_amount": {"5000"}})
	if status != http.StatusBadRequest {
		t.Fatalf("second capture status = %d, want 400 (%v)", status, again)
	}
}

func TestWebhookDeliversChargeComplete(t *testing.T) {
	var (
		mu       sync.Mutex
		received []map[string]any
	)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev map[string]any
		if err := json.NewDecoder(r.Body).Decode(&ev); err == nil {
			mu.Lock()
			received = append(received, ev)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	srv := fakeomise.NewTestServer(t, fakeomise.Options{WebhookURL: sink.URL})
	c := newClient(t, srv.URL)

	tok := c.createToken("4242424242424242")
	customer, _ := c.createCustomer(str(t, tok, "id"))
	form := url.Values{}
	form.Set("amount", "7500")
	form.Set("currency", "THB")
	form.Set("customer", str(t, customer, "id"))
	form.Set("return_uri", "https://merchant.example/complete")
	charge, _ := c.charge(form)
	c.authorize(str(t, charge, "id"), "success")
	srv.API.WaitForWebhooks(2 * time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := false
		for _, ev := range received {
			if ev["key"] == "charge.complete" {
				data, _ := ev["data"].(map[string]any)
				if data != nil && data["status"] == "successful" {
					found = true
				}
			}
		}
		mu.Unlock()
		if found {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no charge.complete webhook received; got %d events", len(received))
}

func TestCustomerCardManagement(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{})
	c := newClient(t, srv.URL)

	customer, firstCard := c.createCustomer(str(t, c.createToken("4242424242424242"), "id"))
	customerID := str(t, customer, "id")

	// A second card becomes selectable but not the default.
	added, status := c.post("/customers/"+customerID+"/cards", url.Values{
		"card": {str(t, c.createToken("5555555555554444"), "id")},
	})
	if status != http.StatusOK {
		t.Fatalf("add card status = %d: %v", status, added)
	}
	secondCardID := added["data"].([]any)[1].(map[string]any)["id"].(string)

	updated, _, _ := c.do(http.MethodPatch, "/customers/"+customerID, url.Values{"default_card": {secondCardID}})
	if got := str(t, updated, "default_card"); got != secondCardID {
		t.Fatalf("default_card = %q, want %s", got, secondCardID)
	}

	// Deleting the default promotes the remaining card.
	deleted, status, _ := c.do(http.MethodDelete, "/customers/"+customerID+"/cards/"+secondCardID, nil)
	if status != http.StatusOK || !boolean(t, deleted, "deleted") {
		t.Fatalf("delete card: status %d: %v", status, deleted)
	}
	after, _ := c.get("/customers/" + customerID)
	if got := str(t, after, "default_card"); got != str(t, firstCard, "id") {
		t.Fatalf("default_card after delete = %q, want %s", got, firstCard["id"])
	}
}

func TestChargingAnotherCustomersCardIsRejected(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{})
	c := newClient(t, srv.URL)

	_, aliceCard := c.createCustomer(str(t, c.createToken("4242424242424242"), "id"))
	bob, _ := c.createCustomer(str(t, c.createToken("5555555555554444"), "id"))

	form := url.Values{}
	form.Set("amount", "1000")
	form.Set("currency", "THB")
	form.Set("customer", str(t, bob, "id"))
	form.Set("card", str(t, aliceCard, "id"))
	body, status := c.charge(form)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%v)", status, body)
	}
}

func TestAuthenticationRejectsLiveKeys(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/charges", nil)
	req.SetBasicAuth("skey_live_abc", "")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "authentication_failure" {
		t.Fatalf("code = %v, want authentication_failure", body["code"])
	}
}

// TestZeroAmountModeOnlyChallengesValidationCharges covers the --3ds=zero_amount switch.
func TestZeroAmountModeOnlyChallengesValidationCharges(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{ThreeDS: "zero_amount"})
	c := newClient(t, srv.URL)

	tok := c.createToken("4242424242424242")
	customer, _ := c.createCustomer(str(t, tok, "id"))
	customerID := str(t, customer, "id")

	form := url.Values{}
	form.Set("amount", "1000")
	form.Set("currency", "THB")
	form.Set("customer", customerID)
	plain, _ := c.charge(form)
	if got := str(t, plain, "status"); got != "successful" {
		t.Fatalf("plain charge status = %q, want successful", got)
	}

	form = url.Values{}
	form.Set("amount", "0")
	form.Set("currency", "THB")
	form.Set("customer", customerID)
	form.Set("return_uri", "https://merchant.example/complete")
	validation, _ := c.charge(form)
	if got := str(t, validation, "status"); got != "pending" {
		t.Fatalf("validation charge status = %q, want pending", got)
	}
}

func TestChargeNotFound(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{})
	c := newClient(t, srv.URL)
	body, status := c.get("/charges/chrg_test_missing")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if body["code"] != "not_found" {
		t.Fatalf("code = %v, want not_found", body["code"])
	}
}

func TestListChargesPaginates(t *testing.T) {
	srv := fakeomise.NewTestServer(t, fakeomise.Options{ThreeDS: "off"})
	c := newClient(t, srv.URL)

	for i := 0; i < 3; i++ {
		form := url.Values{}
		form.Set("amount", fmt.Sprintf("%d", 1000+i))
		form.Set("currency", "THB")
		form.Set("card", str(t, c.createToken("4242424242424242"), "id"))
		if _, status := c.charge(form); status != http.StatusOK {
			t.Fatalf("charge %d failed with %d", i, status)
		}
	}
	list, status := c.get("/charges?limit=2")
	if status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	if got := number(t, list, "total"); got != 3 {
		t.Fatalf("total = %d, want 3", got)
	}
	data := list["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("len(data) = %d, want 2", len(data))
	}
	if got := number(t, list, "limit"); got != 2 {
		t.Fatalf("limit = %d, want 2", got)
	}
}
