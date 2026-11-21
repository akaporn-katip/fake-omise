package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/akaporn-katip/fake-omise/internal/omise"
	"github.com/akaporn-katip/fake-omise/internal/store"
	"github.com/akaporn-katip/fake-omise/internal/webhook"
)

// ThreeDS modes.
const (
	ThreeDSAlways     = "always"      // challenge every card that has not been authenticated yet
	ThreeDSZeroAmount = "zero_amount" // only 0-amount validation charges challenge
	ThreeDSOff        = "off"
)

// Config tunes the fake's behaviour.
type Config struct {
	// ThreeDS controls when a charge needs a 3-D Secure challenge.
	ThreeDS string
	// WebhookURL receives event notifications. Empty disables delivery.
	WebhookURL string
	// BaseURL overrides the origin used to build authorize_uri. When empty the
	// request's own scheme and host are used.
	BaseURL string
	// Logger receives delivery and 3DS failures. Defaults to a discard logger.
	Logger *log.Logger
}

// Server implements the fake Omise API.
type Server struct {
	Store   *store.Store
	Config  Config
	handler http.Handler
	hooks   *webhook.Dispatcher
}

// New builds a Server over an existing store.
func New(st *store.Store, cfg Config) *Server {
	if cfg.ThreeDS == "" {
		cfg.ThreeDS = ThreeDSAlways
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(discard{}, "", 0)
	}
	s := &Server{Store: st, Config: cfg}
	s.hooks = webhook.NewDispatcher(st, cfg.WebhookURL, cfg.Logger)
	s.handler = s.routes()
	return s
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// WaitForWebhooks blocks until queued webhook deliveries have been attempted.
func (s *Server) WaitForWebhooks(timeout time.Duration) { s.hooks.Wait(timeout) }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /tokens", s.createToken)

	mux.HandleFunc("POST /customers", s.createCustomer)
	mux.HandleFunc("GET /customers", s.listCustomers)
	mux.HandleFunc("GET /customers/{id}", s.getCustomer)
	mux.HandleFunc("PATCH /customers/{id}", s.updateCustomer)
	mux.HandleFunc("POST /customers/{id}", s.updateCustomer)
	mux.HandleFunc("DELETE /customers/{id}", s.deleteCustomer)

	mux.HandleFunc("GET /customers/{id}/cards", s.listCards)
	mux.HandleFunc("POST /customers/{id}/cards", s.addCard)
	mux.HandleFunc("GET /customers/{id}/cards/{card}", s.getCard)
	mux.HandleFunc("DELETE /customers/{id}/cards/{card}", s.deleteCard)

	mux.HandleFunc("POST /charges", s.createCharge)
	mux.HandleFunc("GET /charges", s.listCharges)
	mux.HandleFunc("GET /charges/{id}", s.getCharge)
	mux.HandleFunc("POST /charges/{id}/capture", s.captureCharge)

	mux.HandleFunc("GET /payments/{id}/authorize", s.authorizationPage)
	mux.HandleFunc("POST /payments/{id}/authorize", s.submitAuthorization)

	mux.HandleFunc("GET /events", s.listEvents)
	mux.HandleFunc("GET /events/{id}", s.getEvent)

	mux.HandleFunc("POST /_fake/reset", s.reset)
	mux.HandleFunc("POST /_fake/charges/{id}/authorize", s.headlessAuthorize)

	return s.withCORS(s.withAuth(mux))
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Fake-3DS, Omise-Version, Omise-Client-Version")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withAuth enforces Omise-style API keys. Any test-mode key is accepted so that
// fixtures do not need to know the fake's configuration; live keys are refused.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The Authorization Page is opened by the cardholder's browser, which
		// has no API key. The unguessable payment id is the capability.
		if strings.HasPrefix(r.URL.Path, "/payments/") {
			next.ServeHTTP(w, r)
			return
		}
		key := ""
		if username, _, ok := r.BasicAuth(); ok {
			key = username
		}
		if key == "" {
			if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
				key = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
			}
		}
		if key == "" {
			writeError(w, http.StatusUnauthorized, omise.NewError("authentication_failure", "authentication failed"))
			return
		}
		if !strings.HasPrefix(key, "skey_test_") && !strings.HasPrefix(key, "pkey_test_") {
			writeError(w, http.StatusUnauthorized, omise.NewError("authentication_failure", "authentication failed: only test keys are accepted"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- request params

// params normalises form-encoded and JSON request bodies.
type params struct {
	form   map[string][]string
	json   map[string]any
	isJSON bool
}

func readParams(r *http.Request) (*params, error) {
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var m map[string]any
		body := http.MaxBytesReader(nil, r.Body, 1<<20)
		if err := json.NewDecoder(body).Decode(&m); err != nil {
			return nil, err
		}
		return &params{json: m, isJSON: true}, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	return &params{form: r.PostForm}, nil
}

func (p *params) str(key string) (string, bool) {
	if p.isJSON {
		v, ok := p.json[key]
		if !ok || v == nil {
			return "", false
		}
		if s, ok := v.(string); ok {
			return s, true
		}
		return fmt.Sprint(v), true
	}
	vs, ok := p.form[key]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}

func (p *params) int64(key string) (int64, bool, error) {
	raw, ok := p.str(key)
	if !ok {
		return 0, false, nil
	}
	switch v := p.raw(key).(type) {
	case float64:
		if v != float64(int64(v)) {
			return 0, true, fmt.Errorf("%s must be an integer", key)
		}
		return int64(v), true, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, true, fmt.Errorf("%s must be an integer", key)
	}
	return n, true, nil
}

func (p *params) raw(key string) any {
	if p.isJSON {
		return p.json[key]
	}
	vs := p.form[key]
	if len(vs) == 0 {
		return nil
	}
	return vs[0]
}

func (p *params) bool(key string, def bool) (bool, error) {
	raw, ok := p.str(key)
	if !ok {
		return def, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean", key)
	}
}

func (p *params) metadata() map[string]any {
	if p.isJSON {
		if m, ok := p.json["metadata"].(map[string]any); ok {
			return m
		}
		return map[string]any{}
	}
	out := map[string]any{}
	for key, vs := range p.form {
		if !strings.HasPrefix(key, "metadata[") || !strings.HasSuffix(key, "]") || len(vs) == 0 {
			continue
		}
		out[strings.TrimSuffix(strings.TrimPrefix(key, "metadata["), "]")] = vs[0]
	}
	return out
}

// ---------------------------------------------------------------- responses

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, e *omise.Error) {
	writeJSON(w, status, e)
}

func (s *Server) notFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, omise.NewError("not_found", "the requested object was not found"))
}

func (s *Server) badRequest(w http.ResponseWriter, code, message string) {
	writeError(w, http.StatusBadRequest, omise.NewError(code, message))
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	s.Config.Logger.Printf("internal error: %v", err)
	writeError(w, http.StatusInternalServerError, omise.NewError("internal_error", "request could not be completed due to an internal error"))
}

// ---------------------------------------------------------------- pagination

type pageQuery struct {
	limit  int
	offset int
	order  string
	from   time.Time
	to     time.Time
}

func readPage(r *http.Request) pageQuery {
	q := pageQuery{limit: 20, order: "chronological"}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			q.limit = min(n, 100)
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			q.offset = n
		}
	}
	if v := r.URL.Query().Get("order"); v == "reverse_chronological" {
		q.order = v
	}
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q.from = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q.to = t
		}
	}
	return q
}

func (q pageQuery) apply(created []time.Time) []int {
	idx := make([]int, 0, len(created))
	for i, t := range created {
		if !q.from.IsZero() && t.Before(q.from) {
			continue
		}
		if !q.to.IsZero() && t.After(q.to) {
			continue
		}
		idx = append(idx, i)
	}
	if q.order == "reverse_chronological" {
		sort.SliceStable(idx, func(a, b int) bool { return created[idx[a]].After(created[idx[b]]) })
	}
	start := min(q.offset, len(idx))
	end := min(start+q.limit, len(idx))
	return idx[start:end]
}

func newList(location, order string, data any, total int) omise.List {
	return omise.List{
		Object:   "list",
		Data:     data,
		Limit:    20,
		Offset:   0,
		Total:    total,
		Location: location,
		Order:    order,
		From:     omise.NewTimestamp(time.Unix(0, 0)),
		To:       omise.NewTimestamp(time.Now()),
	}
}

func errIs(err, target error) bool { return errors.Is(err, target) }

// valueOr dereferences a nullable string, falling back to def.
func valueOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}
