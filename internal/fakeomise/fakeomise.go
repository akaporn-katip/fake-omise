package fakeomise

import (
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/akaporn-katip/fake-omise/internal/api"
	"github.com/akaporn-katip/fake-omise/internal/store"
)

// Options configures a fake server.
type Options struct {
	// ThreeDS is "always" (default), "zero_amount" or "off".
	ThreeDS string
	// WebhookURL receives event notifications.
	WebhookURL string
	// BaseURL overrides the origin used in authorize_uri.
	BaseURL string
	// DBPath is the SQLite file. Empty uses a temporary file that is removed on Close.
	DBPath string
	// Logger receives diagnostics.
	Logger *log.Logger
}

// Server is a running fake Omise API.
type Server struct {
	URL    string
	API    *api.Server
	Store  *store.Store
	http   *httptest.Server
	dbPath string
	ownsDB bool
}

// New starts a fake server on a loopback port.
func New(opts Options) (*Server, error) {
	dbPath := opts.DBPath
	ownsDB := false
	if dbPath == "" {
		dir, err := os.MkdirTemp("", "fake-omise-*")
		if err != nil {
			return nil, err
		}
		dbPath = filepath.Join(dir, "fake-omise.db")
		ownsDB = true
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	handler := api.New(st, api.Config{
		ThreeDS:    opts.ThreeDS,
		WebhookURL: opts.WebhookURL,
		BaseURL:    opts.BaseURL,
		Logger:     opts.Logger,
	})
	ts := httptest.NewUnstartedServer(handler.Handler())
	if opts.BaseURL == "" {
		// Set before Start so no request can observe the change.
		handler.Config.BaseURL = ts.URL
	}
	ts.Start()

	srv := &Server{URL: ts.URL, API: handler, Store: st, http: ts, dbPath: dbPath, ownsDB: ownsDB}
	return srv, nil
}

// NewTestServer starts a fake backed by a fresh database that is removed when
// the test finishes.
func NewTestServer(t testing.TB, opts Options) *Server {
	t.Helper()
	opts.DBPath = filepath.Join(t.TempDir(), "fake-omise.db")
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("start fake omise: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

// Close stops the server and releases the database.
func (s *Server) Close() {
	if s.http != nil {
		s.http.Close()
	}
	if s.Store != nil {
		_ = s.Store.Close()
	}
	if s.ownsDB {
		_ = os.RemoveAll(filepath.Dir(s.dbPath))
	}
}

// HTTPClient returns a client that talks to the fake with a test secret key.
func (s *Server) HTTPClient() *http.Client { return s.http.Client() }
