package api

import (
	"net/http"
	"strings"

	"github.com/akaporn-katip/fake-omise/internal/omise"
	"github.com/akaporn-katip/fake-omise/internal/store"
)

// sub returns a view of a nested parameter object, accepting both
// card[number]=... form encoding and {"card":{"number":...}} JSON.
func (p *params) sub(name string) *params {
	if p.isJSON {
		m, _ := p.json[name].(map[string]any)
		return &params{json: m, isJSON: true}
	}
	prefix := name + "["
	sub := map[string][]string{}
	for k, v := range p.form {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, "]") {
			sub[strings.TrimSuffix(strings.TrimPrefix(k, prefix), "]")] = v
		}
	}
	return &params{form: sub}
}

// createToken implements POST /tokens. A token is a single-use card snapshot.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	p, err := readParams(r)
	if err != nil {
		s.badRequest(w, "bad_request", "could not read request body")
		return
	}
	card, oerr := buildCardSnapshot(p.sub("card"))
	if oerr != nil {
		writeError(w, http.StatusBadRequest, oerr)
		return
	}
	tok, err := s.Store.CreateToken(card)
	if err != nil {
		s.internal(w, err)
		return
	}
	if _, err := s.Store.CreateEvent(false, "token.create", tok); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tok)
}

func buildCardSnapshot(p *params) (*omise.Card, *omise.Error) {
	number, _ := p.str("number")
	digits := omise.Digits(number)
	if len(digits) < 12 || len(digits) > 19 {
		return nil, omise.NewError("invalid_card", "number is invalid")
	}
	month, ok, err := p.int64("expiration_month")
	if !ok || err != nil || month < 1 || month > 12 {
		return nil, omise.NewError("invalid_card", "expiration_month is invalid")
	}
	year, ok, err := p.int64("expiration_year")
	if !ok || err != nil || year < 2000 {
		return nil, omise.NewError("invalid_card", "expiration_year is invalid")
	}
	securityCode, _ := p.str("security_code")
	name, _ := p.str("name")

	spec := omise.LookupTestCard(digits)
	card := &omise.Card{
		Brand:             spec.Brand,
		LastDigits:        omise.LastDigits(digits),
		ExpirationMonth:   int(month),
		ExpirationYear:    int(year),
		Name:              name,
		Fingerprint:       omise.Fingerprint(digits),
		SecurityCodeCheck: len(securityCode) >= 3 && len(securityCode) <= 4,
		ThreeDSBehavior:   string(spec.ThreeDS),
		DeclineCode:       spec.DeclineCode,
	}
	return card, nil
}

// consumeToken resolves a token identifier into its card snapshot, rejecting
// reuse the way Omise does.
func (s *Server) consumeToken(w http.ResponseWriter, id string) (*omise.Card, bool) {
	tok, err := s.Store.ConsumeToken(id)
	switch {
	case errIs(err, store.ErrUsedToken):
		s.badRequest(w, "used_token", "token was already used")
		return nil, false
	case errIs(err, store.ErrNotFound):
		s.notFound(w)
		return nil, false
	case err != nil:
		s.internal(w, err)
		return nil, false
	}
	return tok.Card, true
}
