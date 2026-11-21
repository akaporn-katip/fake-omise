package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/akaporn-katip/fake-omise/internal/store"
)

// listEvents implements GET /events.
func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Store.ListEvents()
	if err != nil {
		s.internal(w, err)
		return
	}
	q := readPage(r)
	q.order = "reverse_chronological"
	writeJSON(w, http.StatusOK, renderEvents(events, q))
}

// getEvent implements GET /events/{id}.
func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	ev, err := s.Store.GetEvent(r.PathValue("id"))
	if errIs(err, store.ErrNotFound) {
		s.notFound(w)
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

// reset implements POST /_fake/reset, wiping every stored object.
func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Reset(); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "fake_reset", "deleted": true})
}

// headlessAuthorize implements POST /_fake/charges/{id}/authorize so automated
// tests can finish a 3DS challenge without a browser.
func (s *Server) headlessAuthorize(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.GetCharge(r.PathValue("id"))
	if errIs(err, store.ErrNotFound) {
		s.notFound(w)
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}

	outcome := r.URL.Query().Get("outcome")
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if len(raw) > 0 {
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			var body struct {
				Outcome string `json:"outcome"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				s.badRequest(w, "bad_request", "body must be JSON like {\"outcome\":\"success\"}")
				return
			}
			if body.Outcome != "" {
				outcome = body.Outcome
			}
		} else {
			form, err := url.ParseQuery(string(raw))
			if err != nil {
				s.badRequest(w, "bad_request", "body must be a form or JSON with an outcome")
				return
			}
			if v := form.Get("outcome"); v != "" {
				outcome = v
			}
		}
	}
	if outcome != "" && outcome != "success" && outcome != "fail" {
		s.badRequest(w, "bad_request", "outcome must be \"success\" or \"fail\"")
		return
	}

	if err := s.completeAuthorization(c, outcome != "fail"); err != nil {
		s.internal(w, err)
		return
	}
	updated, err := s.Store.GetCharge(c.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.renderCharge(updated))
}
