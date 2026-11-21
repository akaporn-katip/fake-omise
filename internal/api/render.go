package api

import (
	"math"
	"net/http"
	"time"

	"github.com/akaporn-katip/fake-omise/internal/omise"
)

const chargeFeeRate = 3.65 // percent, matching Omise THB card pricing
const chargeVATRate = 7.0

// renderCharge fills the derived fields Omise computes at read time.
func (s *Server) renderCharge(c *omise.Charge) *omise.Charge {
	if c == nil {
		return nil
	}
	if card, err := s.Store.GetCard(c.CardID); err == nil {
		c.Card = card
	}

	fee := int64(math.Round(float64(c.Amount) * chargeFeeRate / 100))
	vat := int64(math.Round(float64(fee) * chargeVATRate / 100))
	c.Fee = fee
	c.FeeVAT = vat
	c.Net = c.Amount - fee - vat
	c.FundingAmount = c.Amount
	c.FundingCurrency = c.Currency

	c.RefundedAmount = 0
	c.Refundable = c.Paid
	c.Disputable = c.Paid
	c.ZeroInterestInstallments = false
	c.Refunds = omise.List{
		Object:   "list",
		Data:     []any{},
		Limit:    20,
		Offset:   0,
		Total:    0,
		Location: "/charges/" + c.ID + "/refunds",
		Order:    "chronological",
		From:     omise.NewTimestamp(time.Unix(0, 0)),
		To:       omise.NewTimestamp(time.Now()),
	}
	c.Expired = c.Status == omise.StatusExpired
	return c
}

// renderCharges renders a page of charges.
func (s *Server) renderCharges(charges []*omise.Charge, q pageQuery) omise.List {
	created := make([]time.Time, len(charges))
	for i, c := range charges {
		created[i] = c.CreatedAt.Time
	}
	idx := q.apply(created)
	data := make([]*omise.Charge, 0, len(idx))
	for _, i := range idx {
		data = append(data, s.renderCharge(charges[i]))
	}
	list := newList("/charges", q.order, data, len(charges))
	list.Limit, list.Offset = q.limit, q.offset
	return list
}

// renderCustomer fills the customer's card list.
func (s *Server) renderCustomer(c *omise.Customer, cards []*omise.Card) *omise.Customer {
	if c == nil {
		return nil
	}
	if cards == nil {
		cards, _ = s.Store.ListCards(c.ID)
	}
	if c.Metadata == nil {
		c.Metadata = map[string]any{}
	}
	c.Cards = omise.List{
		Object:   "list",
		Data:     cards,
		Limit:    20,
		Offset:   0,
		Total:    len(cards),
		Location: "/customers/" + c.ID + "/cards",
		Order:    "chronological",
		From:     omise.NewTimestamp(time.Unix(0, 0)),
		To:       omise.NewTimestamp(time.Now()),
	}
	return c
}

// renderCustomers renders a page of customers.
func (s *Server) renderCustomers(customers []*omise.Customer, q pageQuery) omise.List {
	created := make([]time.Time, len(customers))
	for i, c := range customers {
		created[i] = c.CreatedAt.Time
	}
	idx := q.apply(created)
	data := make([]*omise.Customer, 0, len(idx))
	for _, i := range idx {
		data = append(data, s.renderCustomer(customers[i], nil))
	}
	list := newList("/customers", q.order, data, len(customers))
	list.Limit, list.Offset = q.limit, q.offset
	return list
}

// renderCards renders a page of cards belonging to a customer.
func renderCards(customerID string, cards []*omise.Card, q pageQuery) omise.List {
	created := make([]time.Time, len(cards))
	for i, c := range cards {
		created[i] = c.CreatedAt.Time
	}
	idx := q.apply(created)
	data := make([]*omise.Card, 0, len(idx))
	for _, i := range idx {
		card := cards[i]
		loc := "/customers/" + customerID + "/cards/" + card.ID
		card.Location = &loc
		data = append(data, card)
	}
	list := newList("/customers/"+customerID+"/cards", q.order, data, len(cards))
	list.Limit, list.Offset = q.limit, q.offset
	return list
}

// renderEvents renders a page of events, newest first.
func renderEvents(events []*omise.Event, q pageQuery) omise.List {
	created := make([]time.Time, len(events))
	for i, e := range events {
		created[i] = e.CreatedAt.Time
	}
	idx := q.apply(created)
	data := make([]*omise.Event, 0, len(idx))
	for _, i := range idx {
		if events[i].WebhookDeliveries == nil {
			events[i].WebhookDeliveries = []omise.WebhookDelivery{}
		}
		data = append(data, events[i])
	}
	list := newList("/events", q.order, data, len(events))
	list.Limit, list.Offset = q.limit, q.offset
	return list
}

func (s *Server) origin(r *http.Request) string {
	if s.Config.BaseURL != "" {
		return s.Config.BaseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}
