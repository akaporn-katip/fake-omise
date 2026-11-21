package api

import (
	"net/http"

	"github.com/akaporn-katip/fake-omise/internal/omise"
	"github.com/akaporn-katip/fake-omise/internal/store"
)

// createCustomer implements POST /customers.
func (s *Server) createCustomer(w http.ResponseWriter, r *http.Request) {
	p, err := readParams(r)
	if err != nil {
		s.badRequest(w, "bad_request", "could not read request body")
		return
	}
	email, _ := p.str("email")
	description, _ := p.str("description")

	c := &omise.Customer{
		ID:          omise.NewID("cust"),
		Email:       email,
		Description: description,
		Metadata:    p.metadata(),
	}

	// Consume the token before creating anything, so an invalid or reused token
	// cannot leave a half-built customer behind.
	var snapshot *omise.Card
	if tokenID, ok := p.str("card"); ok && tokenID != "" {
		var valid bool
		if snapshot, valid = s.consumeToken(w, tokenID); !valid {
			return
		}
	}

	if err := s.Store.CreateCustomer(c); err != nil {
		s.internal(w, err)
		return
	}
	if snapshot != nil {
		if err := s.attachCard(c, snapshot); err != nil {
			s.internal(w, err)
			return
		}
	}
	s.emit("customer.create", c)
	s.writeCustomer(w, c)
}

// attachCard stores a card snapshot under a customer and makes it the default
// when the customer has no default yet.
func (s *Server) attachCard(c *omise.Customer, snapshot *omise.Card) error {
	card, err := s.Store.AttachCardToCustomer(c.ID, snapshot)
	if err != nil {
		return err
	}
	if c.DefaultCard == nil {
		def := card.ID
		if _, err := s.Store.UpdateCustomer(c.ID, nil, nil, nil, &def); err != nil {
			return err
		}
		c.DefaultCard = &def
	}
	s.emit("card.create", card)
	return nil
}

// writeCustomer loads a customer's cards and serialises it.
func (s *Server) writeCustomer(w http.ResponseWriter, c *omise.Customer) {
	fresh, err := s.Store.GetCustomer(c.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.renderCustomer(fresh, nil))
}

// listCustomers implements GET /customers.
func (s *Server) listCustomers(w http.ResponseWriter, r *http.Request) {
	customers, err := s.Store.ListCustomers()
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.renderCustomers(customers, readPage(r)))
}

// getCustomer implements GET /customers/{id}.
func (s *Server) getCustomer(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.GetCustomer(r.PathValue("id"))
	if errIs(err, store.ErrNotFound) {
		s.notFound(w)
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	s.writeCustomer(w, c)
}

// updateCustomer implements PATCH /customers/{id} and POST /customers/{id}.
func (s *Server) updateCustomer(w http.ResponseWriter, r *http.Request) {
	p, err := readParams(r)
	if err != nil {
		s.badRequest(w, "bad_request", "could not read request body")
		return
	}
	var email, description, defaultCard *string
	if v, ok := p.str("email"); ok {
		email = &v
	}
	if v, ok := p.str("description"); ok {
		description = &v
	}
	if v, ok := p.str("default_card"); ok {
		defaultCard = &v
	}
	var metadata map[string]any
	if _, hasJSON := p.json["metadata"]; (p.isJSON && hasJSON) || len(p.metadata()) > 0 {
		metadata = p.metadata()
	}

	c, err := s.Store.UpdateCustomer(r.PathValue("id"), email, description, metadata, defaultCard)
	if errIs(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, omise.NewError("invalid_customer", "default card does not belong to this customer"))
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	s.emit("customer.update", s.renderCustomer(c, nil))
	s.writeCustomer(w, c)
}

// deleteCustomer implements DELETE /customers/{id}.
func (s *Server) deleteCustomer(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.DeleteCustomer(r.PathValue("id"))
	if errIs(err, store.ErrNotFound) {
		s.notFound(w)
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	s.emit("customer.destroy", s.renderCustomer(c, nil))
	s.writeCustomer(w, c)
}

// listCards implements GET /customers/{id}/cards.
func (s *Server) listCards(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Store.GetCustomer(id); err != nil {
		s.notFound(w)
		return
	}
	cards, err := s.Store.ListCards(id)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, renderCards(id, cards, readPage(r)))
}

// addCard implements POST /customers/{id}/cards.
func (s *Server) addCard(w http.ResponseWriter, r *http.Request) {
	p, err := readParams(r)
	if err != nil {
		s.badRequest(w, "bad_request", "could not read request body")
		return
	}
	c, err := s.Store.GetCustomer(r.PathValue("id"))
	if err != nil {
		s.notFound(w)
		return
	}
	tokenID, ok := p.str("card")
	if !ok || tokenID == "" {
		s.badRequest(w, "missing_card", "request contains no card parameters")
		return
	}
	snapshot, ok := s.consumeToken(w, tokenID)
	if !ok {
		return
	}
	if err := s.attachCard(c, snapshot); err != nil {
		s.internal(w, err)
		return
	}
	cards, err := s.Store.ListCards(c.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, renderCards(c.ID, cards, readPage(r)))
}

// getCard implements GET /customers/{id}/cards/{card}.
func (s *Server) getCard(w http.ResponseWriter, r *http.Request) {
	card, err := s.Store.GetCard(r.PathValue("card"))
	if errIs(err, store.ErrNotFound) || (err == nil && card.CustomerID != r.PathValue("id")) {
		s.notFound(w)
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	loc := "/customers/" + card.CustomerID + "/cards/" + card.ID
	card.Location = &loc
	writeJSON(w, http.StatusOK, card)
}

// deleteCard implements DELETE /customers/{id}/cards/{card}.
func (s *Server) deleteCard(w http.ResponseWriter, r *http.Request) {
	card, err := s.Store.DeleteCard(r.PathValue("id"), r.PathValue("card"))
	if errIs(err, store.ErrNotFound) {
		s.notFound(w)
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	s.emit("card.destroy", card)
	writeJSON(w, http.StatusOK, card)
}
