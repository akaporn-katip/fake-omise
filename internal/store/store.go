package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/akaporn-katip/fake-omise/internal/omise"
)

// ErrNotFound is returned when an identifier does not exist.
var ErrNotFound = errors.New("not found")

// Store is the SQLite persistence layer. It is safe for concurrent use; SQLite
// is serialised through a single connection because the dataset is tiny.
type Store struct {
	mu sync.Mutex
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS tokens (
	id          TEXT PRIMARY KEY,
	livemode    INTEGER NOT NULL,
	used        INTEGER NOT NULL DEFAULT 0,
	card_id     TEXT NOT NULL,
	created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS customers (
	id              TEXT PRIMARY KEY,
	livemode        INTEGER NOT NULL,
	email           TEXT NOT NULL DEFAULT '',
	description     TEXT NOT NULL DEFAULT '',
	metadata        TEXT NOT NULL DEFAULT '{}',
	default_card_id TEXT,
	deleted         INTEGER NOT NULL DEFAULT 0,
	created_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS cards (
	id                 TEXT PRIMARY KEY,
	livemode           INTEGER NOT NULL,
	customer_id        TEXT NOT NULL DEFAULT '',
	token_id           TEXT NOT NULL DEFAULT '',
	brand              TEXT NOT NULL,
	last_digits        TEXT NOT NULL,
	expiration_month   INTEGER NOT NULL,
	expiration_year    INTEGER NOT NULL,
	name               TEXT NOT NULL DEFAULT '',
	fingerprint        TEXT NOT NULL,
	security_code_check INTEGER NOT NULL DEFAULT 1,
	three_ds_behavior  TEXT NOT NULL DEFAULT 'challenge',
	decline_code       TEXT NOT NULL DEFAULT '',
	deleted            INTEGER NOT NULL DEFAULT 0,
	created_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS cards_customer ON cards(customer_id);
CREATE INDEX IF NOT EXISTS cards_fingerprint ON cards(fingerprint);

CREATE TABLE IF NOT EXISTS authenticated_fingerprints (
	fingerprint      TEXT PRIMARY KEY,
	charge_id        TEXT NOT NULL,
	authenticated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS charges (
	id               TEXT PRIMARY KEY,
	livemode         INTEGER NOT NULL,
	status           TEXT NOT NULL,
	amount           INTEGER NOT NULL,
	currency         TEXT NOT NULL,
	capture          INTEGER NOT NULL,
	card_id          TEXT NOT NULL DEFAULT '',
	customer_id      TEXT NOT NULL DEFAULT '',
	description      TEXT NOT NULL DEFAULT '',
	metadata         TEXT NOT NULL DEFAULT '{}',
	return_uri       TEXT NOT NULL DEFAULT '',
	authorize_uri    TEXT NOT NULL DEFAULT '',
	payment_id       TEXT NOT NULL DEFAULT '',
	requires_3ds     INTEGER NOT NULL DEFAULT 0,
	authorized       INTEGER NOT NULL DEFAULT 0,
	paid             INTEGER NOT NULL DEFAULT 0,
	capturable       INTEGER NOT NULL DEFAULT 0,
	authorized_amount INTEGER NOT NULL DEFAULT 0,
	captured_amount  INTEGER NOT NULL DEFAULT 0,
	failure_code     TEXT NOT NULL DEFAULT '',
	failure_message  TEXT NOT NULL DEFAULT '',
	approval_code    TEXT NOT NULL DEFAULT '',
	authenticated_by TEXT NOT NULL DEFAULT '',
	ip               TEXT NOT NULL DEFAULT '',
	transaction_id   TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL,
	authorized_at    TEXT NOT NULL DEFAULT '',
	paid_at          TEXT NOT NULL DEFAULT '',
	expires_at       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS charges_created ON charges(created_at);

CREATE TABLE IF NOT EXISTS events (
	id         TEXT PRIMARY KEY,
	livemode   INTEGER NOT NULL,
	key        TEXT NOT NULL,
	data       TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_created ON events(created_at);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
	id         TEXT PRIMARY KEY,
	event_id   TEXT NOT NULL,
	uri        TEXT NOT NULL,
	status     INTEGER NOT NULL DEFAULT 0,
	error      TEXT NOT NULL DEFAULT '',
	payload    TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
`

// Open creates (or opens) the database at path and applies the schema.
// Use ":memory:" for an ephemeral store.
func Open(path string) (*Store, error) {
	dsn := path
	if path == ":memory:" {
		dsn = "file::memory:?cache=shared"
	} else {
		// SQLite URI filenames need forward slashes, including on Windows.
		dsn = "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if path != ":memory:" {
		if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Reset deletes every row, leaving the schema in place.
func (s *Store) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, table := range []string{"tokens", "customers", "cards", "authenticated_fingerprints", "charges", "events", "webhook_deliveries"} {
		if _, err := s.db.Exec("DELETE FROM " + table); err != nil {
			return err
		}
	}
	return nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }
func now() time.Time        { return time.Now().UTC() }
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
func i2b(i int) bool { return i != 0 }
func nstr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func parseTime(s string) omise.Timestamp {
	if s == "" {
		return omise.Timestamp{}
	}
	return omise.ParseTimestamp(s)
}

func timePtr(s string) *omise.Timestamp {
	if s == "" {
		return nil
	}
	t := omise.ParseTimestamp(s)
	return &t
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---------------------------------------------------------------- tokens

// CreateToken stores a token and the card snapshot it was created from.
func (s *Store) CreateToken(card *omise.Card) (*omise.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	card.ID = omise.NewID("card")
	card.Object = "card"
	card.CreatedAt = omise.NewTimestamp(now())
	card.SecurityCodeCheck = true
	card.Financing = "credit"
	card.CustomerID = ""

	created := now()
	if err := s.insertCard(card); err != nil {
		return nil, err
	}
	tok := &omise.Token{
		Object:    "token",
		ID:        omise.NewID("tokn"),
		Livemode:  false,
		Location:  "",
		Card:      card,
		CreatedAt: omise.NewTimestamp(created),
	}
	tok.Location = "/tokens/" + tok.ID

	if _, err := s.db.Exec(
		`INSERT INTO tokens (id, livemode, used, card_id, created_at) VALUES (?,?,?,?,?)`,
		tok.ID, b2i(tok.Livemode), 0, card.ID, ts(created),
	); err != nil {
		return nil, err
	}
	return tok, nil
}

// GetToken loads a token by identifier.
func (s *Store) GetToken(id string) (*omise.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getToken(id)
}

func (s *Store) getToken(id string) (*omise.Token, error) {
	var (
		livemode, used int
		cardID         string
		createdAt      string
	)
	err := s.db.QueryRow(`SELECT livemode, used, card_id, created_at FROM tokens WHERE id=?`, id).
		Scan(&livemode, &used, &cardID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	card, err := s.getCard(cardID)
	if err != nil {
		return nil, err
	}
	return &omise.Token{
		Object:    "token",
		ID:        id,
		Livemode:  i2b(livemode),
		Location:  "/tokens/" + id,
		Used:      i2b(used),
		Card:      card,
		CreatedAt: parseTime(createdAt),
	}, nil
}

// ConsumeToken marks a token used and returns it. A token can only be consumed once.
func (s *Store) ConsumeToken(id string) (*omise.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, err := s.getToken(id)
	if err != nil {
		return nil, err
	}
	if tok.Used {
		return nil, ErrUsedToken
	}
	if _, err := s.db.Exec(`UPDATE tokens SET used=1 WHERE id=?`, id); err != nil {
		return nil, err
	}
	tok.Used = true
	return tok, nil
}

// ErrUsedToken reports an attempt to reuse a single-use token.
var ErrUsedToken = errors.New("token was already used")

// ---------------------------------------------------------------- cards

func (s *Store) insertCard(c *omise.Card) error {
	_, err := s.db.Exec(
		`INSERT INTO cards (id, livemode, customer_id, token_id, brand, last_digits, expiration_month,
		 expiration_year, name, fingerprint, security_code_check, three_ds_behavior, decline_code,
		 deleted, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, b2i(c.Livemode), c.CustomerID, c.TokenID, c.Brand, c.LastDigits, c.ExpirationMonth,
		c.ExpirationYear, c.Name, c.Fingerprint, b2i(c.SecurityCodeCheck), c.ThreeDSBehavior,
		c.DeclineCode, b2i(c.Deleted), ts(c.CreatedAt.Time),
	)
	return err
}

func (s *Store) getCard(id string) (*omise.Card, error) {
	row := s.db.QueryRow(
		`SELECT c.id, c.livemode, c.customer_id, c.token_id, c.brand, c.last_digits, c.expiration_month,
		 c.expiration_year, c.name, c.fingerprint, c.security_code_check, c.three_ds_behavior,
		 c.decline_code, c.deleted, c.created_at, f.authenticated_at
		 FROM cards c LEFT JOIN authenticated_fingerprints f ON f.fingerprint = c.fingerprint
		 WHERE c.id=?`, id)
	return scanCard(row)
}

type scannable interface {
	Scan(dest ...any) error
}

func scanCard(row scannable) (*omise.Card, error) {
	var (
		c                      omise.Card
		livemode, scs, deleted int
		created                string
		authenticatedAt        sql.NullString
	)
	err := row.Scan(&c.ID, &livemode, &c.CustomerID, &c.TokenID, &c.Brand, &c.LastDigits,
		&c.ExpirationMonth, &c.ExpirationYear, &c.Name, &c.Fingerprint, &scs, &c.ThreeDSBehavior,
		&c.DeclineCode, &deleted, &created, &authenticatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Object = "card"
	c.Financing = "credit"
	c.Livemode = i2b(livemode)
	c.SecurityCodeCheck = i2b(scs)
	c.Deleted = i2b(deleted)
	c.CreatedAt = parseTime(created)
	c.ThreeDSAuthenticatedAt = timePtr(nstr(authenticatedAt))
	return &c, nil
}

// GetCard loads a card by identifier.
func (s *Store) GetCard(id string) (*omise.Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCard(id)
}

// AttachCardToCustomer moves a card snapshot onto a customer, giving it a new id.
func (s *Store) AttachCardToCustomer(customerID string, src *omise.Card) (*omise.Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	card := *src
	card.ID = omise.NewID("card")
	card.CustomerID = customerID
	card.CreatedAt = omise.NewTimestamp(now())
	if err := s.insertCard(&card); err != nil {
		return nil, err
	}
	return &card, nil
}

// ListCards returns a customer's live (non-deleted) cards.
func (s *Store) ListCards(customerID string) ([]*omise.Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT c.id, c.livemode, c.customer_id, c.token_id, c.brand, c.last_digits,
		 c.expiration_month, c.expiration_year, c.name, c.fingerprint, c.security_code_check,
		 c.three_ds_behavior, c.decline_code, c.deleted, c.created_at, f.authenticated_at
		 FROM cards c LEFT JOIN authenticated_fingerprints f ON f.fingerprint = c.fingerprint
		 WHERE c.customer_id=? AND c.deleted=0 ORDER BY c.created_at`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*omise.Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteCard soft-deletes a customer's card and promotes another default card.
func (s *Store) DeleteCard(customerID, cardID string) (*omise.Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	card, err := s.getCard(cardID)
	if err != nil {
		return nil, err
	}
	if card.CustomerID != customerID {
		return nil, ErrNotFound
	}
	if _, err := s.db.Exec(`UPDATE cards SET deleted=1 WHERE id=?`, cardID); err != nil {
		return nil, err
	}
	card.Deleted = true

	var def sql.NullString
	if err := s.db.QueryRow(`SELECT default_card_id FROM customers WHERE id=?`, customerID).Scan(&def); err != nil {
		return nil, err
	}
	if def.String == cardID {
		var next sql.NullString
		err := s.db.QueryRow(`SELECT id FROM cards WHERE customer_id=? AND deleted=0 ORDER BY created_at LIMIT 1`, customerID).Scan(&next)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if _, err := s.db.Exec(`UPDATE customers SET default_card_id=? WHERE id=?`, next, customerID); err != nil {
			return nil, err
		}
	}
	return card, nil
}

// ---------------------------------------------------------------- customers

// CreateCustomer stores a customer.
func (s *Store) CreateCustomer(c *omise.Customer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		c.ID = omise.NewID("cust")
	}
	c.Object = "customer"
	c.Location = "/customers/" + c.ID
	c.CreatedAt = omise.NewTimestamp(now())
	var def any
	if c.DefaultCard != nil {
		def = *c.DefaultCard
	}
	_, err := s.db.Exec(
		`INSERT INTO customers (id, livemode, email, description, metadata, default_card_id, deleted, created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		c.ID, b2i(c.Livemode), c.Email, c.Description, omise.EncodeMetadata(c.Metadata), def, b2i(c.Deleted), ts(c.CreatedAt.Time),
	)
	return err
}

// GetCustomer loads a customer by identifier.
func (s *Store) GetCustomer(id string) (*omise.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCustomer(id)
}

func (s *Store) getCustomer(id string) (*omise.Customer, error) {
	var (
		c                 omise.Customer
		livemode, deleted int
		metadata, created string
		def               sql.NullString
	)
	err := s.db.QueryRow(
		`SELECT id, livemode, email, description, metadata, default_card_id, deleted, created_at
		 FROM customers WHERE id=?`, id).
		Scan(&c.ID, &livemode, &c.Email, &c.Description, &metadata, &def, &deleted, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Object = "customer"
	c.Livemode = i2b(livemode)
	c.Deleted = i2b(deleted)
	c.Metadata = omise.Metadata(metadata)
	c.CreatedAt = parseTime(created)
	c.Location = "/customers/" + c.ID
	if def.Valid {
		c.DefaultCard = &def.String
	}
	return &c, nil
}

// ListCustomers returns every customer.
func (s *Store) ListCustomers() ([]*omise.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT id, livemode, email, description, metadata, default_card_id, deleted, created_at
		 FROM customers ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*omise.Customer
	for rows.Next() {
		var (
			c                 omise.Customer
			livemode, deleted int
			metadata, created string
			def               sql.NullString
		)
		if err := rows.Scan(&c.ID, &livemode, &c.Email, &c.Description, &metadata, &def, &deleted, &created); err != nil {
			return nil, err
		}
		c.Object = "customer"
		c.Livemode = i2b(livemode)
		c.Deleted = i2b(deleted)
		c.Metadata = omise.Metadata(metadata)
		c.CreatedAt = parseTime(created)
		c.Location = "/customers/" + c.ID
		if def.Valid {
			c.DefaultCard = &def.String
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// UpdateCustomer applies a partial update: email, description, metadata, default card.
func (s *Store) UpdateCustomer(id string, email, description *string, metadata map[string]any, defaultCard *string) (*omise.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, err := s.getCustomer(id)
	if err != nil {
		return nil, err
	}
	if email != nil {
		c.Email = *email
	}
	if description != nil {
		c.Description = *description
	}
	if metadata != nil {
		c.Metadata = metadata
	}
	if defaultCard != nil {
		card, err := s.getCard(*defaultCard)
		if err != nil || card.CustomerID != id || card.Deleted {
			return nil, ErrNotFound
		}
		c.DefaultCard = defaultCard
	}
	var def any
	if c.DefaultCard != nil {
		def = *c.DefaultCard
	}
	if _, err := s.db.Exec(
		`UPDATE customers SET email=?, description=?, metadata=?, default_card_id=? WHERE id=?`,
		c.Email, c.Description, omise.EncodeMetadata(c.Metadata), def, id,
	); err != nil {
		return nil, err
	}
	return c, nil
}

// DeleteCustomer soft-deletes a customer.
func (s *Store) DeleteCustomer(id string) (*omise.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.getCustomer(id)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`UPDATE customers SET deleted=1, default_card_id=NULL WHERE id=?`, id); err != nil {
		return nil, err
	}
	c.Deleted = true
	c.DefaultCard = nil
	return c, nil
}

// ---------------------------------------------------------------- 3ds authentication

// AuthenticateFingerprint records that the instrument behind fingerprint passed 3DS.
func (s *Store) AuthenticateFingerprint(fingerprint, chargeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO authenticated_fingerprints (fingerprint, charge_id, authenticated_at) VALUES (?,?,?)
		 ON CONFLICT(fingerprint) DO UPDATE SET charge_id=excluded.charge_id, authenticated_at=excluded.authenticated_at`,
		fingerprint, chargeID, ts(now()),
	)
	return err
}

// IsFingerprintAuthenticated reports whether a card number has already passed 3DS.
func (s *Store) IsFingerprintAuthenticated(fingerprint string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM authenticated_fingerprints WHERE fingerprint=?`, fingerprint).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ---------------------------------------------------------------- charges

// CreateCharge persists a new charge.
func (s *Store) CreateCharge(c *omise.Charge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insertCharge(c)
}

func (s *Store) insertCharge(c *omise.Charge) error {
	var (
		customerID, cardID string

		failureCode, failureMessage, approval, authBy, ip, txn string
		authorizedAt, paidAt, expiresAt                        string
	)
	if c.CustomerID != nil {
		customerID = *c.CustomerID
	}
	if c.Card != nil {
		cardID = c.Card.ID
	}
	if c.FailureCode != nil {
		failureCode = *c.FailureCode
	}
	if c.FailureMessage != nil {
		failureMessage = *c.FailureMessage
	}
	if c.ApprovalCode != nil {
		approval = *c.ApprovalCode
	}
	if c.AuthenticatedBy != nil {
		authBy = *c.AuthenticatedBy
	}
	if c.IP != nil {
		ip = *c.IP
	}
	if c.Transaction != nil {
		txn = *c.Transaction
	}
	if c.AuthorizedAt != nil {
		authorizedAt = ts(c.AuthorizedAt.Time)
	}
	if c.PaidAt != nil {
		paidAt = ts(c.PaidAt.Time)
	}
	if c.ExpiresAt != nil {
		expiresAt = ts(c.ExpiresAt.Time)
	}
	_, err := s.db.Exec(
		`INSERT INTO charges (id, livemode, status, amount, currency, capture, card_id, customer_id,
		 description, metadata, return_uri, authorize_uri, payment_id, requires_3ds, authorized, paid,
		 capturable, authorized_amount, captured_amount, failure_code, failure_message, approval_code,
		 authenticated_by, ip, transaction_id, created_at, authorized_at, paid_at, expires_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, b2i(c.Livemode), c.Status, c.Amount, c.Currency, b2i(c.Capture), cardID, customerID,
		valueOr(c.Description, ""), omise.EncodeMetadata(c.Metadata), valueOr(c.ReturnURI, ""),
		valueOr(c.AuthorizeURI, ""), c.PaymentID, b2i(c.Requires3DS), b2i(c.Authorized), b2i(c.Paid),
		b2i(c.Capturable), c.AuthorizedAmount, c.CapturedAmount, failureCode, failureMessage, approval,
		authBy, ip, txn, ts(c.CreatedAt.Time), authorizedAt, paidAt, expiresAt,
	)
	return err
}

func valueOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

// UpdateCharge rewrites a charge's mutable state.
func (s *Store) UpdateCharge(c *omise.Charge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		failureCode, failureMessage, approval, authBy string
		authorizedAt, paidAt                          string
	)
	if c.FailureCode != nil {
		failureCode = *c.FailureCode
	}
	if c.FailureMessage != nil {
		failureMessage = *c.FailureMessage
	}
	if c.ApprovalCode != nil {
		approval = *c.ApprovalCode
	}
	if c.AuthenticatedBy != nil {
		authBy = *c.AuthenticatedBy
	}
	if c.AuthorizedAt != nil {
		authorizedAt = ts(c.AuthorizedAt.Time)
	}
	if c.PaidAt != nil {
		paidAt = ts(c.PaidAt.Time)
	}
	_, err := s.db.Exec(
		`UPDATE charges SET status=?, authorized=?, paid=?, capturable=?, authorized_amount=?,
		 captured_amount=?, failure_code=?, failure_message=?, approval_code=?, authenticated_by=?,
		 authorized_at=?, paid_at=?, transaction_id=? WHERE id=?`,
		c.Status, b2i(c.Authorized), b2i(c.Paid), b2i(c.Capturable), c.AuthorizedAmount,
		c.CapturedAmount, failureCode, failureMessage, approval, authBy, authorizedAt, paidAt,
		valueOr(c.Transaction, ""), c.ID,
	)
	return err
}

// GetCharge loads a charge by identifier.
func (s *Store) GetCharge(id string) (*omise.Charge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCharge(id)
}

func (s *Store) getCharge(id string) (*omise.Charge, error) {
	row := s.db.QueryRow(
		`SELECT id, livemode, status, amount, currency, capture, card_id, customer_id, description,
		 metadata, return_uri, authorize_uri, payment_id, requires_3ds, authorized, paid, capturable,
		 authorized_amount, captured_amount, failure_code, failure_message, approval_code,
		 authenticated_by, ip, transaction_id, created_at, authorized_at, paid_at, expires_at
		 FROM charges WHERE id=?`, id)
	return scanCharge(row)
}

func scanCharge(row scannable) (*omise.Charge, error) {
	var (
		c                                                                             omise.Charge
		livemode, capture, req3ds, authorized, paid, capturable                       int
		cardID, customerID, description, metadata, returnURI, authorizeURI, paymentID string
		failureCode, failureMessage, approval, authBy, ip, txn, created               string
		authorizedAt, paidAt, expiresAt                                               string
	)
	err := row.Scan(&c.ID, &livemode, &c.Status, &c.Amount, &c.Currency, &capture, &cardID,
		&customerID, &description, &metadata, &returnURI, &authorizeURI, &paymentID, &req3ds,
		&authorized, &paid, &capturable, &c.AuthorizedAmount, &c.CapturedAmount, &failureCode,
		&failureMessage, &approval, &authBy, &ip, &txn, &created, &authorizedAt, &paidAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Object = "charge"
	c.Livemode = i2b(livemode)
	c.Capture = i2b(capture)
	c.Requires3DS = i2b(req3ds)
	c.Authorized = i2b(authorized)
	c.Paid = i2b(paid)
	c.Capturable = i2b(capturable)
	c.Description = strPtr(description)
	c.Metadata = omise.Metadata(metadata)
	c.ReturnURI = strPtr(returnURI)
	c.AuthorizeURI = strPtr(authorizeURI)
	c.PaymentID = paymentID
	c.FailureCode = strPtr(failureCode)
	c.FailureMessage = strPtr(failureMessage)
	c.ApprovalCode = strPtr(approval)
	c.AuthenticatedBy = strPtr(authBy)
	c.IP = strPtr(ip)
	c.Transaction = strPtr(txn)
	c.CreatedAt = parseTime(created)
	c.AuthorizedAt = timePtr(authorizedAt)
	c.PaidAt = timePtr(paidAt)
	c.ExpiresAt = timePtr(expiresAt)
	c.CardID = cardID
	if customerID != "" {
		c.CustomerID = &customerID
	}
	c.Location = "/charges/" + c.ID
	return &c, nil
}

// ListCharges returns charges oldest first.
func (s *Store) ListCharges() ([]*omise.Charge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT id, livemode, status, amount, currency, capture, card_id, customer_id, description,
		 metadata, return_uri, authorize_uri, payment_id, requires_3ds, authorized, paid, capturable,
		 authorized_amount, captured_amount, failure_code, failure_message, approval_code,
		 authenticated_by, ip, transaction_id, created_at, authorized_at, paid_at, expires_at
		 FROM charges ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*omise.Charge
	for rows.Next() {
		c, err := scanCharge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- events

// CreateEvent persists an event and returns it.
func (s *Store) CreateEvent(livemode bool, key string, data any) (*omise.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	payload, err := jsonMarshal(data)
	if err != nil {
		return nil, err
	}
	ev := &omise.Event{
		Object:    "event",
		ID:        omise.NewID("evnt"),
		Livemode:  livemode,
		Key:       key,
		CreatedAt: omise.NewTimestamp(now()),
		Data:      data,
	}
	ev.Location = "/events/" + ev.ID
	if _, err := s.db.Exec(
		`INSERT INTO events (id, livemode, key, data, created_at) VALUES (?,?,?,?,?)`,
		ev.ID, b2i(livemode), key, payload, ts(ev.CreatedAt.Time),
	); err != nil {
		return nil, err
	}
	return ev, nil
}

// ListEvents returns events, optionally only those after a timestamp.
func (s *Store) ListEvents() ([]*omise.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, livemode, key, data, created_at FROM events ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*omise.Event
	for rows.Next() {
		var (
			id, key, data, created string
			livemode               int
		)
		if err := rows.Scan(&id, &livemode, &key, &data, &created); err != nil {
			return nil, err
		}
		ev := &omise.Event{
			Object:    "event",
			ID:        id,
			Livemode:  i2b(livemode),
			Location:  "/events/" + id,
			Key:       key,
			CreatedAt: parseTime(created),
		}
		_ = jsonUnmarshal(data, &ev.Data)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// GetEvent loads an event by identifier.
func (s *Store) GetEvent(id string) (*omise.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		key, data, created string
		livemode           int
	)
	err := s.db.QueryRow(`SELECT livemode, key, data, created_at FROM events WHERE id=?`, id).
		Scan(&livemode, &key, &data, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ev := &omise.Event{
		Object:    "event",
		ID:        id,
		Livemode:  i2b(livemode),
		Location:  "/events/" + id,
		Key:       key,
		CreatedAt: parseTime(created),
	}
	_ = jsonUnmarshal(data, &ev.Data)
	deliveries, err := s.listDeliveries(id)
	if err != nil {
		return nil, err
	}
	ev.WebhookDeliveries = deliveries
	return ev, nil
}

func (s *Store) listDeliveries(eventID string) ([]omise.WebhookDelivery, error) {
	rows, err := s.db.Query(
		`SELECT id, uri, status, error, payload, created_at FROM webhook_deliveries WHERE event_id=? ORDER BY created_at`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []omise.WebhookDelivery
	for rows.Next() {
		var d omise.WebhookDelivery
		var created string
		if err := rows.Scan(&d.ID, &d.URI, &d.Status, &d.Error, &d.Payload, &created); err != nil {
			return nil, err
		}
		d.Object = "webhook_delivery"
		d.CreatedAt = parseTime(created)
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecordDelivery stores one webhook delivery attempt.
func (s *Store) RecordDelivery(eventID string, d omise.WebhookDelivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO webhook_deliveries (id, event_id, uri, status, error, payload, created_at) VALUES (?,?,?,?,?,?,?)`,
		d.ID, eventID, d.URI, d.Status, d.Error, d.Payload, ts(d.CreatedAt.Time),
	)
	return err
}

// GetChargeByPaymentID finds the charge behind a 3DS authorization id.
func (s *Store) GetChargeByPaymentID(paymentID string) (*omise.Charge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(
		`SELECT id, livemode, status, amount, currency, capture, card_id, customer_id, description,
		 metadata, return_uri, authorize_uri, payment_id, requires_3ds, authorized, paid, capturable,
		 authorized_amount, captured_amount, failure_code, failure_message, approval_code,
		 authenticated_by, ip, transaction_id, created_at, authorized_at, paid_at, expires_at
		 FROM charges WHERE payment_id=?`, paymentID)
	return scanCharge(row)
}
