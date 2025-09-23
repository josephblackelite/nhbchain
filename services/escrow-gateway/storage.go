package main

import (
	"context"
	"database/sql"
	"errors"
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
