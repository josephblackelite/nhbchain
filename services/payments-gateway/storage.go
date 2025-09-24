package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

// ErrIdempotencyConflict indicates a key is reused with a different payload.
var ErrIdempotencyConflict = errors.New("idempotency key conflict")

// SQLiteStore persists quotes, invoices, and audit logs.
type SQLiteStore struct {
	db *sql.DB
}

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
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS idempotency_keys (
            key TEXT PRIMARY KEY,
            request_hash TEXT NOT NULL,
            response_status INTEGER NOT NULL,
            response_body BLOB NOT NULL,
            created_at TIMESTAMP NOT NULL
        );`,
		`CREATE TABLE IF NOT EXISTS audit_log (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            occurred_at TIMESTAMP NOT NULL,
            method TEXT NOT NULL,
            path TEXT NOT NULL,
            request_body BLOB,
            response_status INTEGER,
            response_body BLOB
        );`,
		`CREATE TABLE IF NOT EXISTS quotes (
            id TEXT PRIMARY KEY,
            fiat_currency TEXT NOT NULL,
            token TEXT NOT NULL,
            amount_fiat TEXT NOT NULL,
            amount_token TEXT NOT NULL,
            expiry TIMESTAMP NOT NULL,
            created_at TIMESTAMP NOT NULL
        );`,
		`CREATE TABLE IF NOT EXISTS invoices (
            id TEXT PRIMARY KEY,
            quote_id TEXT NOT NULL,
            recipient TEXT NOT NULL,
            status TEXT NOT NULL,
            nowpayments_id TEXT,
            nowpayments_url TEXT,
            tx_hash TEXT,
            created_at TIMESTAMP NOT NULL,
            updated_at TIMESTAMP NOT NULL,
            UNIQUE(quote_id)
        );`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

// StoredResponse captures an idempotent response.
type StoredResponse struct {
	Status int
	Body   []byte
}

func (s *SQLiteStore) LookupIdempotency(ctx context.Context, key, hash string) (*StoredResponse, error) {
	const query = `SELECT response_status, response_body, request_hash FROM idempotency_keys WHERE key = ?`
	row := s.db.QueryRowContext(ctx, query, key)
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
	if storedHash != hash {
		return nil, ErrIdempotencyConflict
	}
	return &StoredResponse{Status: status, Body: body}, nil
}

func (s *SQLiteStore) SaveIdempotency(ctx context.Context, key, hash string, status int, body []byte) error {
	const stmt = `INSERT OR REPLACE INTO idempotency_keys(key, request_hash, response_status, response_body, created_at) VALUES (?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, key, hash, status, body, time.Now().UTC())
	return err
}

// AuditEntry captures request/response pairs.
type AuditEntry struct {
	Method         string
	Path           string
	RequestBody    []byte
	ResponseStatus int
	ResponseBody   []byte
	Timestamp      time.Time
}

func (s *SQLiteStore) InsertAudit(ctx context.Context, entry AuditEntry) error {
	const stmt = `INSERT INTO audit_log(occurred_at, method, path, request_body, response_status, response_body) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, entry.Timestamp, entry.Method, entry.Path, entry.RequestBody, entry.ResponseStatus, entry.ResponseBody)
	return err
}

// QuoteRecord describes a quote persisted in SQLite.
type QuoteRecord struct {
	ID           string
	FiatCurrency string
	Token        string
	AmountFiat   string
	AmountToken  string
	Expiry       time.Time
	CreatedAt    time.Time
}

func (s *SQLiteStore) InsertQuote(ctx context.Context, q QuoteRecord) error {
	const stmt = `INSERT INTO quotes(id, fiat_currency, token, amount_fiat, amount_token, expiry, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, q.ID, q.FiatCurrency, q.Token, q.AmountFiat, q.AmountToken, q.Expiry, q.CreatedAt)
	return err
}

func (s *SQLiteStore) GetQuote(ctx context.Context, id string) (*QuoteRecord, error) {
	const query = `SELECT id, fiat_currency, token, amount_fiat, amount_token, expiry, created_at FROM quotes WHERE id = ?`
	row := s.db.QueryRowContext(ctx, query, id)
	var rec QuoteRecord
	if err := row.Scan(&rec.ID, &rec.FiatCurrency, &rec.Token, &rec.AmountFiat, &rec.AmountToken, &rec.Expiry, &rec.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &rec, nil
}

// InvoiceRecord captures stored invoice metadata.
type InvoiceRecord struct {
	ID        string
	QuoteID   string
	Recipient string
	Status    string
	NowID     string
	NowURL    string
	TxHash    sql.NullString
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *SQLiteStore) InsertInvoice(ctx context.Context, inv InvoiceRecord) error {
	const stmt = `INSERT INTO invoices(id, quote_id, recipient, status, nowpayments_id, nowpayments_url, tx_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, inv.ID, inv.QuoteID, inv.Recipient, inv.Status, inv.NowID, inv.NowURL, inv.TxHash, inv.CreatedAt, inv.UpdatedAt)
	return err
}

func (s *SQLiteStore) GetInvoice(ctx context.Context, id string) (*InvoiceRecord, error) {
	const query = `SELECT id, quote_id, recipient, status, nowpayments_id, nowpayments_url, tx_hash, created_at, updated_at FROM invoices WHERE id = ?`
	row := s.db.QueryRowContext(ctx, query, id)
	return scanInvoice(row)
}

func (s *SQLiteStore) GetInvoiceByNowID(ctx context.Context, nowID string) (*InvoiceRecord, error) {
	const query = `SELECT id, quote_id, recipient, status, nowpayments_id, nowpayments_url, tx_hash, created_at, updated_at FROM invoices WHERE nowpayments_id = ?`
	row := s.db.QueryRowContext(ctx, query, nowID)
	return scanInvoice(row)
}

func scanInvoice(row *sql.Row) (*InvoiceRecord, error) {
	var rec InvoiceRecord
	err := row.Scan(&rec.ID, &rec.QuoteID, &rec.Recipient, &rec.Status, &rec.NowID, &rec.NowURL, &rec.TxHash, &rec.CreatedAt, &rec.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *SQLiteStore) UpdateInvoiceStatus(ctx context.Context, id, status string, txHash *string) error {
	const stmt = `UPDATE invoices SET status = ?, tx_hash = ?, updated_at = ? WHERE id = ?`
	var hash interface{}
	if txHash != nil {
		hash = *txHash
	} else {
		hash = nil
	}
	_, err := s.db.ExecContext(ctx, stmt, status, hash, time.Now().UTC(), id)
	return err
}

// MarshalInvoice converts an InvoiceRecord into a JSON-friendly payload.
func MarshalInvoice(inv *InvoiceRecord, quote *QuoteRecord) ([]byte, error) {
	if inv == nil {
		return json.Marshal(map[string]string{"error": "invoice not found"})
	}
	payload := map[string]interface{}{
		"invoiceId": inv.ID,
		"quoteId":   inv.QuoteID,
		"recipient": inv.Recipient,
		"status":    inv.Status,
		"nowpayments": map[string]string{
			"id":  inv.NowID,
			"url": inv.NowURL,
		},
		"updatedAt": inv.UpdatedAt.UTC().Format(time.RFC3339),
		"createdAt": inv.CreatedAt.UTC().Format(time.RFC3339),
	}
	if quote != nil {
		payload["amountFiat"] = quote.AmountFiat
		payload["amountToken"] = quote.AmountToken
	}
	if inv.TxHash.Valid {
		payload["txHash"] = inv.TxHash.String
	}
	return json.Marshal(payload)
}
