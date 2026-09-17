package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore manages idempotency keys and audit log persistence.
type SQLiteStore struct {
	db *sql.DB
}

// ErrIdempotencyMismatch is returned when a key is reused with a different payload.
var ErrIdempotencyMismatch = errors.New("idempotency key reuse with different request body")

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{db: db}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) init() error {
	schema := []string{
		`CREATE TABLE IF NOT EXISTS idempotency_keys (
            api_key TEXT NOT NULL,
            idempotency_key TEXT NOT NULL,
            request_hash TEXT NOT NULL,
            response_status INTEGER NOT NULL,
            response_body BLOB NOT NULL,
            created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
            PRIMARY KEY(api_key, idempotency_key)
        );`,
		`CREATE TABLE IF NOT EXISTS audit_log (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            occurred_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
            api_key TEXT,
            method TEXT NOT NULL,
            path TEXT NOT NULL,
            request_body BLOB,
            response_status INTEGER,
            response_body BLOB
        );`,
		`CREATE TABLE IF NOT EXISTS p2p_offers (
            id TEXT PRIMARY KEY,
            seller TEXT NOT NULL,
            base_token TEXT NOT NULL,
            base_amount TEXT NOT NULL,
            quote_token TEXT NOT NULL,
            quote_amount TEXT NOT NULL,
            min_quote TEXT,
            max_quote TEXT,
            terms TEXT,
            active INTEGER NOT NULL,
            created_at TIMESTAMP NOT NULL
        );`,
		`CREATE TABLE IF NOT EXISTS p2p_trades (
            id TEXT PRIMARY KEY,
            offer_id TEXT NOT NULL,
            buyer TEXT NOT NULL,
            seller TEXT NOT NULL,
            base_token TEXT NOT NULL,
            base_amount TEXT NOT NULL,
            quote_token TEXT NOT NULL,
            quote_amount TEXT NOT NULL,
            escrow_base_id TEXT NOT NULL,
            escrow_quote_id TEXT NOT NULL,
            status TEXT NOT NULL,
            created_at TIMESTAMP NOT NULL,
            updated_at TIMESTAMP NOT NULL
        );`,
		`CREATE TABLE IF NOT EXISTS trade_escrows (
            escrow_id TEXT PRIMARY KEY,
            trade_id TEXT NOT NULL
        );`,
		`CREATE TABLE IF NOT EXISTS events (
            sequence INTEGER PRIMARY KEY,
            type TEXT NOT NULL,
            height INTEGER NOT NULL,
            tx_hash TEXT,
            payload TEXT NOT NULL,
            created_at TIMESTAMP NOT NULL
        );`,
		`CREATE TABLE IF NOT EXISTS event_cursors (
            name TEXT PRIMARY KEY,
            value INTEGER NOT NULL
        );`,
		`CREATE TABLE IF NOT EXISTS webhooks (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            api_key TEXT NOT NULL,
            event_type TEXT NOT NULL,
            url TEXT NOT NULL,
            secret TEXT NOT NULL,
            rate_limit INTEGER NOT NULL DEFAULT 60,
            active INTEGER NOT NULL DEFAULT 1,
            created_at TIMESTAMP NOT NULL
        );`,
		`CREATE TABLE IF NOT EXISTS webhook_attempts (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            webhook_id INTEGER NOT NULL,
            event_sequence INTEGER NOT NULL,
            attempt INTEGER NOT NULL,
            status TEXT NOT NULL,
            error TEXT,
            next_attempt TIMESTAMP,
            created_at TIMESTAMP NOT NULL
        );`,
	}
	for _, stmt := range schema {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// StoredResponse represents a cached response for an idempotency key.
type StoredResponse struct {
	Status int
	Body   []byte
}

func (s *SQLiteStore) LookupIdempotency(ctx context.Context, apiKey, key, requestHash string) (*StoredResponse, error) {
	const query = `SELECT response_status, response_body, request_hash FROM idempotency_keys WHERE api_key = ? AND idempotency_key = ?`
	row := s.db.QueryRowContext(ctx, query, apiKey, key)
	var status int
	var body []byte
	var storedHash string
	err := row.Scan(&status, &body, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if storedHash != requestHash {
		return nil, ErrIdempotencyMismatch
	}
	return &StoredResponse{Status: status, Body: body}, nil
}

func (s *SQLiteStore) SaveIdempotency(ctx context.Context, apiKey, key, requestHash string, status int, body []byte) error {
	const stmt = `INSERT OR REPLACE INTO idempotency_keys(api_key, idempotency_key, request_hash, response_status, response_body, created_at) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, apiKey, key, requestHash, status, body, time.Now().UTC())
	return err
}

func (s *SQLiteStore) InsertAuditLog(ctx context.Context, entry AuditEntry) error {
	const stmt = `INSERT INTO audit_log(api_key, method, path, request_body, response_status, response_body, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, entry.APIKey, entry.Method, entry.Path, entry.RequestBody, entry.ResponseStatus, entry.ResponseBody, entry.Timestamp)
	return err
}

// P2POffer represents a stored marketplace offer.
type P2POffer struct {
	ID          string    `json:"offerId"`
	Seller      string    `json:"seller"`
	BaseToken   string    `json:"baseToken"`
	BaseAmount  string    `json:"baseAmount"`
	QuoteToken  string    `json:"quoteToken"`
	QuoteAmount string    `json:"quoteAmount"`
	MinQuote    string    `json:"minAmount,omitempty"`
	MaxQuote    string    `json:"maxAmount,omitempty"`
	Terms       string    `json:"terms,omitempty"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"createdAt"`
}

// InsertOffer persists a new marketplace offer.
func (s *SQLiteStore) InsertOffer(ctx context.Context, offer P2POffer) error {
	const stmt = `INSERT INTO p2p_offers(id, seller, base_token, base_amount, quote_token, quote_amount, min_quote, max_quote, terms, active, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	active := 0
	if offer.Active {
		active = 1
	}
	_, err := s.db.ExecContext(ctx, stmt, offer.ID, offer.Seller, offer.BaseToken, offer.BaseAmount, offer.QuoteToken, offer.QuoteAmount, offer.MinQuote, offer.MaxQuote, offer.Terms, active, offer.CreatedAt)
	return err
}

// ListOffers returns all stored offers ordered by creation time descending.
func (s *SQLiteStore) ListOffers(ctx context.Context) ([]P2POffer, error) {
	const query = `SELECT id, seller, base_token, base_amount, quote_token, quote_amount, min_quote, max_quote, terms, active, created_at FROM p2p_offers ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var offers []P2POffer
	for rows.Next() {
		var offer P2POffer
		var active int
		if err := rows.Scan(&offer.ID, &offer.Seller, &offer.BaseToken, &offer.BaseAmount, &offer.QuoteToken, &offer.QuoteAmount, &offer.MinQuote, &offer.MaxQuote, &offer.Terms, &active, &offer.CreatedAt); err != nil {
			return nil, err
		}
		offer.Active = active == 1
		offers = append(offers, offer)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return offers, nil
}

// GetOffer fetches an offer by identifier.
func (s *SQLiteStore) GetOffer(ctx context.Context, id string) (P2POffer, error) {
	const query = `SELECT id, seller, base_token, base_amount, quote_token, quote_amount, min_quote, max_quote, terms, active, created_at FROM p2p_offers WHERE id = ?`
	row := s.db.QueryRowContext(ctx, query, id)
	var offer P2POffer
	var active int
	if err := row.Scan(&offer.ID, &offer.Seller, &offer.BaseToken, &offer.BaseAmount, &offer.QuoteToken, &offer.QuoteAmount, &offer.MinQuote, &offer.MaxQuote, &offer.Terms, &active, &offer.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return P2POffer{}, fmt.Errorf("offer %s not found", id)
		}
		return P2POffer{}, err
	}
	offer.Active = active == 1
	return offer, nil
}

// P2PTrade represents a stored trade record.
type P2PTrade struct {
	ID            string    `json:"id"`
	OfferID       string    `json:"offerId"`
	Buyer         string    `json:"buyer"`
	Seller        string    `json:"seller"`
	BaseToken     string    `json:"baseToken"`
	BaseAmount    string    `json:"baseAmount"`
	QuoteToken    string    `json:"quoteToken"`
	QuoteAmount   string    `json:"quoteAmount"`
	EscrowBaseID  string    `json:"escrowBaseId"`
	EscrowQuoteID string    `json:"escrowQuoteId"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// InsertTrade records a new trade.
func (s *SQLiteStore) InsertTrade(ctx context.Context, trade P2PTrade) error {
	const stmt = `INSERT INTO p2p_trades(id, offer_id, buyer, seller, base_token, base_amount, quote_token, quote_amount, escrow_base_id, escrow_quote_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, trade.ID, trade.OfferID, trade.Buyer, trade.Seller, trade.BaseToken, trade.BaseAmount, trade.QuoteToken, trade.QuoteAmount, trade.EscrowBaseID, trade.EscrowQuoteID, trade.Status, trade.CreatedAt, trade.UpdatedAt)
	return err
}

// UpdateTradeStatus updates the status and timestamp of a trade.
func (s *SQLiteStore) UpdateTradeStatus(ctx context.Context, tradeID, status string, updatedAt time.Time) error {
	const stmt = `UPDATE p2p_trades SET status = ?, updated_at = ? WHERE id = ?`
	res, err := s.db.ExecContext(ctx, stmt, status, updatedAt, tradeID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("trade %s not found", tradeID)
	}
	return nil
}

// GetTrade fetches a trade by ID.
func (s *SQLiteStore) GetTrade(ctx context.Context, id string) (P2PTrade, error) {
	const query = `SELECT id, offer_id, buyer, seller, base_token, base_amount, quote_token, quote_amount, escrow_base_id, escrow_quote_id, status, created_at, updated_at FROM p2p_trades WHERE id = ?`
	row := s.db.QueryRowContext(ctx, query, id)
	var trade P2PTrade
	if err := row.Scan(&trade.ID, &trade.OfferID, &trade.Buyer, &trade.Seller, &trade.BaseToken, &trade.BaseAmount, &trade.QuoteToken, &trade.QuoteAmount, &trade.EscrowBaseID, &trade.EscrowQuoteID, &trade.Status, &trade.CreatedAt, &trade.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return P2PTrade{}, fmt.Errorf("trade %s not found", id)
		}
		return P2PTrade{}, err
	}
	return trade, nil
}

// LinkEscrowToTrade stores a mapping from escrow identifier to trade identifier.
func (s *SQLiteStore) LinkEscrowToTrade(ctx context.Context, escrowID, tradeID string) error {
	const stmt = `INSERT OR REPLACE INTO trade_escrows(escrow_id, trade_id) VALUES(?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, escrowID, tradeID)
	return err
}

// TradeIDByEscrow resolves the trade id given an escrow id.
func (s *SQLiteStore) TradeIDByEscrow(ctx context.Context, escrowID string) (string, error) {
	const query = `SELECT trade_id FROM trade_escrows WHERE escrow_id = ?`
	row := s.db.QueryRowContext(ctx, query, escrowID)
	var tradeID string
	if err := row.Scan(&tradeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("no trade mapping for escrow %s", escrowID)
		}
		return "", err
	}
	return tradeID, nil
}

// StoredEvent represents an event persisted to SQLite.
type StoredEvent struct {
	Sequence  int64
	Type      string
	Height    uint64
	TxHash    string
	Payload   map[string]string
	CreatedAt time.Time
}

// InsertEvent inserts an event row.
func (s *SQLiteStore) InsertEvent(ctx context.Context, evt StoredEvent) error {
	const stmt = `INSERT OR REPLACE INTO events(sequence, type, height, tx_hash, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`
	payloadJSON, err := json.Marshal(evt.Payload)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, stmt, evt.Sequence, evt.Type, evt.Height, evt.TxHash, string(payloadJSON), evt.CreatedAt)
	return err
}

// LastEventSequence returns the last processed event sequence.
func (s *SQLiteStore) LastEventSequence(ctx context.Context) (int64, error) {
	const query = `SELECT value FROM event_cursors WHERE name = 'events'`
	row := s.db.QueryRowContext(ctx, query)
	var value int64
	if err := row.Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return value, nil
}

// UpdateEventSequence stores the last processed event sequence.
func (s *SQLiteStore) UpdateEventSequence(ctx context.Context, sequence int64) error {
	const stmt = `INSERT INTO event_cursors(name, value) VALUES('events', ?) ON CONFLICT(name) DO UPDATE SET value = excluded.value`
	_, err := s.db.ExecContext(ctx, stmt, sequence)
	return err
}

// WebhookSubscription describes a stored webhook endpoint.
type WebhookSubscription struct {
	ID        int64
	APIKey    string
	EventType string
	URL       string
	Secret    string
	RateLimit int
	Active    bool
	CreatedAt time.Time
}

// InsertWebhook registers a webhook subscription.
func (s *SQLiteStore) InsertWebhook(ctx context.Context, sub WebhookSubscription) (int64, error) {
	const stmt = `INSERT INTO webhooks(api_key, event_type, url, secret, rate_limit, active, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	active := 0
	if sub.Active {
		active = 1
	}
	res, err := s.db.ExecContext(ctx, stmt, sub.APIKey, sub.EventType, sub.URL, sub.Secret, sub.RateLimit, active, sub.CreatedAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// ListWebhooksForEvent returns subscriptions interested in a given event type.
func (s *SQLiteStore) ListWebhooksForEvent(ctx context.Context, eventType string) ([]WebhookSubscription, error) {
	const query = `SELECT id, api_key, event_type, url, secret, rate_limit, active, created_at FROM webhooks WHERE event_type = ?`
	rows, err := s.db.QueryContext(ctx, query, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var subs []WebhookSubscription
	for rows.Next() {
		var sub WebhookSubscription
		var active int
		if err := rows.Scan(&sub.ID, &sub.APIKey, &sub.EventType, &sub.URL, &sub.Secret, &sub.RateLimit, &active, &sub.CreatedAt); err != nil {
			return nil, err
		}
		sub.Active = active == 1
		if sub.RateLimit <= 0 {
			sub.RateLimit = 60
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return subs, nil
}

// WebhookAttempt captures a delivery attempt.
type WebhookAttempt struct {
	WebhookID     int64
	EventSequence int64
	Attempt       int
	Status        string
	Error         string
	NextAttempt   time.Time
	CreatedAt     time.Time
}

// InsertWebhookAttempt records a webhook delivery attempt.
func (s *SQLiteStore) InsertWebhookAttempt(ctx context.Context, attempt WebhookAttempt) error {
	const stmt = `INSERT INTO webhook_attempts(webhook_id, event_sequence, attempt, status, error, next_attempt, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, attempt.WebhookID, attempt.EventSequence, attempt.Attempt, attempt.Status, attempt.Error, nullTime(attempt.NextAttempt), attempt.CreatedAt)
	return err
}

func nullTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}

// AuditEntry represents an audit log row.
type AuditEntry struct {
	APIKey         string
	Method         string
	Path           string
	RequestBody    []byte
	ResponseBody   []byte
	ResponseStatus int
	Timestamp      time.Time
}
