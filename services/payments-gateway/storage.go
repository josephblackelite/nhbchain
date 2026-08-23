package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	sqlitedriver "modernc.org/sqlite"
)

// ErrIdempotencyConflict indicates a key is reused with a different payload.
var ErrIdempotencyConflict = errors.New("idempotency key conflict")

// ErrPaymentSlotClaimed indicates InsertPayment lost the race for a
// (quote_id, pay_currency) slot: idx_payments_active_quote_currency already
// has a non-terminal row for that pair -- either a genuinely outstanding
// payment to reuse, or another request's in-flight claim. Callers must not
// treat this as a generic failure; see resolvePayment in server.go for how
// it's used to close the payment-creation TOCTOU race.
var ErrPaymentSlotClaimed = errors.New("payment slot already claimed")

// sqliteConstraintUnique is SQLITE_CONSTRAINT_UNIQUE. modernc.org/sqlite
// enables extended result codes on every connection it opens (see its
// conn.extendedResultCodes(true) call in newConn), so a real unique-index
// violation from that driver always surfaces with this exact code.
const sqliteConstraintUnique = 2067

// isUniqueConstraintErr reports whether err is a SQLite UNIQUE constraint
// violation -- specifically, for this file's purposes, a collision against
// idx_payments_active_quote_currency.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlitedriver.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqliteConstraintUnique {
		return true
	}
	// Fallback for any other SQLite error shape: this exact text is emitted
	// directly by SQLite's own sqlite3_errmsg() and has been stable across
	// SQLite versions/bindings for years, so it's a safe secondary signal
	// if the typed-error check above ever misses (e.g. a future driver
	// upgrade that changes how errors are wrapped).
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// SQLiteStore persists quotes, invoices, and audit logs.
type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite allows only one writer at a time, and this DSN has no
	// busy-timeout PRAGMA configured, so a second connection that hits a
	// write lock already held by another fails immediately with
	// SQLITE_BUSY instead of waiting. Capping the pool at a single
	// connection instead routes every concurrent caller (reads and writes
	// alike) through Go's own connection queue, turning lock contention
	// into a bounded wait rather than a sporadic error. This matters now
	// that InsertPayment's unique-index claim (see
	// idx_payments_active_quote_currency below) is relied on to correctly
	// serialize concurrent payment-creation requests -- that guarantee only
	// holds if concurrent claim attempts reliably surface as a clean
	// constraint-violation error rather than occasionally as SQLITE_BUSY.
	db.SetMaxOpenConns(1)
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
		// payments tracks headless (deposit-address) NOWPayments payments,
		// the sibling of invoices for the checkout-URL-free flow. Unlike
		// invoices, quote_id is deliberately NOT unique: the idempotent-reuse
		// policy allows a fresh payment row for the same quote once a prior
		// attempt in that currency is terminal, or whenever a different
		// currency is chosen, so a quote can have multiple payment attempts
		// over its lifetime.
		`CREATE TABLE IF NOT EXISTS payments (
            id TEXT PRIMARY KEY,
            quote_id TEXT NOT NULL,
            recipient TEXT NOT NULL,
            status TEXT NOT NULL,
            nowpayments_id TEXT,
            pay_currency TEXT NOT NULL,
            pay_address TEXT,
            pay_amount TEXT,
            payin_extra_id TEXT,
            tx_hash TEXT,
            created_at TIMESTAMP NOT NULL,
            updated_at TIMESTAMP NOT NULL
        );`,
		`CREATE INDEX IF NOT EXISTS idx_payments_quote_currency ON payments(quote_id, pay_currency);`,
		`CREATE INDEX IF NOT EXISTS idx_payments_nowpayments_id ON payments(nowpayments_id);`,
		// idx_payments_active_quote_currency is what actually closes the
		// payment-creation TOCTOU race described in InsertPayment/
		// resolvePayment: a partial UNIQUE index scoped to non-terminal
		// statuses (the exact same set isTerminalPaymentStatus in server.go
		// treats as terminal) means SQLite itself enforces "at most one
		// non-terminal payment row per (quote_id, pay_currency)". A
		// concurrent second INSERT attempting to claim the same slot while
		// a non-terminal row already exists fails against this index,
		// atomically, rather than racing past an application-level
		// SELECT-then-INSERT check. A terminal prior attempt (or none at
		// all) doesn't match the WHERE predicate, so it's excluded from the
		// index and a fresh claim for that same pair succeeds normally.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_active_quote_currency
            ON payments(quote_id, pay_currency)
            WHERE status NOT IN ('finished', 'failed', 'expired', 'refunded', 'minted', 'error');`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	quoteColumns := map[string]string{
		"mint_asset":           "TEXT NOT NULL DEFAULT ''",
		"pay_currency":         "TEXT NOT NULL DEFAULT ''",
		"service_fee_fiat":     "TEXT NOT NULL DEFAULT '0'",
		"total_fiat":           "TEXT NOT NULL DEFAULT '0'",
		"estimated_pay_amount": "TEXT NOT NULL DEFAULT ''",
	}
	for name, def := range quoteColumns {
		if err := s.ensureColumn("quotes", name, def); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) ensureColumn(table, column, definition string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid      int
			name     string
			colType  string
			notNull  int
			defaultV sql.NullString
			primary  int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultV, &primary); err != nil {
			return err
		}
		if strings.EqualFold(name, column) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
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
	ID                 string
	FiatCurrency       string
	Token              string
	MintAsset          string
	PayCurrency        string
	AmountFiat         string
	ServiceFeeFiat     string
	TotalFiat          string
	AmountToken        string
	EstimatedPayAmount string
	Expiry             time.Time
	CreatedAt          time.Time
}

func (s *SQLiteStore) InsertQuote(ctx context.Context, q QuoteRecord) error {
	const stmt = `INSERT INTO quotes(id, fiat_currency, token, mint_asset, pay_currency, amount_fiat, service_fee_fiat, total_fiat, amount_token, estimated_pay_amount, expiry, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, q.ID, q.FiatCurrency, q.Token, q.MintAsset, q.PayCurrency, q.AmountFiat, q.ServiceFeeFiat, q.TotalFiat, q.AmountToken, q.EstimatedPayAmount, q.Expiry, q.CreatedAt)
	return err
}

func (s *SQLiteStore) GetQuote(ctx context.Context, id string) (*QuoteRecord, error) {
	const query = `SELECT id, fiat_currency, token, mint_asset, pay_currency, amount_fiat, service_fee_fiat, total_fiat, amount_token, estimated_pay_amount, expiry, created_at FROM quotes WHERE id = ?`
	row := s.db.QueryRowContext(ctx, query, id)
	var rec QuoteRecord
	if err := row.Scan(&rec.ID, &rec.FiatCurrency, &rec.Token, &rec.MintAsset, &rec.PayCurrency, &rec.AmountFiat, &rec.ServiceFeeFiat, &rec.TotalFiat, &rec.AmountToken, &rec.EstimatedPayAmount, &rec.Expiry, &rec.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(rec.MintAsset) == "" {
		rec.MintAsset = rec.Token
	}
	if strings.TrimSpace(rec.PayCurrency) == "" {
		rec.PayCurrency = rec.Token
	}
	if strings.TrimSpace(rec.TotalFiat) == "" {
		rec.TotalFiat = rec.AmountFiat
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

// InvoiceView joins invoice and quote state for reconciliation/reporting.
type InvoiceView struct {
	ID                 string
	QuoteID            string
	Recipient          string
	Status             string
	NowID              string
	NowURL             string
	TxHash             sql.NullString
	CreatedAt          time.Time
	UpdatedAt          time.Time
	FiatCurrency       string
	Token              string
	MintAsset          string
	PayCurrency        string
	AmountFiat         string
	ServiceFeeFiat     string
	TotalFiat          string
	AmountToken        string
	EstimatedPayAmount string
	QuoteExpiry        time.Time
}

// InvoiceListFilter constrains invoice reconciliation queries.
type InvoiceListFilter struct {
	Status      string
	Recipient   string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	UpdatedFrom *time.Time
	UpdatedTo   *time.Time
	Limit       int
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

// ListInvoiceViews returns invoice reconciliation rows joined with their originating quotes.
func (s *SQLiteStore) ListInvoiceViews(ctx context.Context, filter InvoiceListFilter) ([]InvoiceView, error) {
	query := `
SELECT i.id, i.quote_id, i.recipient, i.status, i.nowpayments_id, i.nowpayments_url, i.tx_hash, i.created_at, i.updated_at,
       q.fiat_currency, q.token, q.mint_asset, q.pay_currency, q.amount_fiat, q.service_fee_fiat, q.total_fiat, q.amount_token, q.estimated_pay_amount, q.expiry
FROM invoices i
JOIN quotes q ON q.id = i.quote_id
`
	clauses := make([]string, 0, 6)
	args := make([]interface{}, 0, 6)
	if status := strings.TrimSpace(filter.Status); status != "" {
		clauses = append(clauses, "i.status = ?")
		args = append(args, status)
	}
	if recipient := strings.TrimSpace(filter.Recipient); recipient != "" {
		clauses = append(clauses, "i.recipient = ?")
		args = append(args, recipient)
	}
	if filter.CreatedFrom != nil {
		clauses = append(clauses, "i.created_at >= ?")
		args = append(args, filter.CreatedFrom.UTC())
	}
	if filter.CreatedTo != nil {
		clauses = append(clauses, "i.created_at <= ?")
		args = append(args, filter.CreatedTo.UTC())
	}
	if filter.UpdatedFrom != nil {
		clauses = append(clauses, "i.updated_at >= ?")
		args = append(args, filter.UpdatedFrom.UTC())
	}
	if filter.UpdatedTo != nil {
		clauses = append(clauses, "i.updated_at <= ?")
		args = append(args, filter.UpdatedTo.UTC())
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY i.updated_at DESC, i.created_at DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]InvoiceView, 0)
	for rows.Next() {
		var item InvoiceView
		if err := rows.Scan(
			&item.ID,
			&item.QuoteID,
			&item.Recipient,
			&item.Status,
			&item.NowID,
			&item.NowURL,
			&item.TxHash,
			&item.CreatedAt,
			&item.UpdatedAt,
			&item.FiatCurrency,
			&item.Token,
			&item.MintAsset,
			&item.PayCurrency,
			&item.AmountFiat,
			&item.ServiceFeeFiat,
			&item.TotalFiat,
			&item.AmountToken,
			&item.EstimatedPayAmount,
			&item.QuoteExpiry,
		); err != nil {
			return nil, err
		}
		if strings.TrimSpace(item.MintAsset) == "" {
			item.MintAsset = item.Token
		}
		if strings.TrimSpace(item.PayCurrency) == "" {
			item.PayCurrency = item.Token
		}
		if strings.TrimSpace(item.TotalFiat) == "" {
			item.TotalFiat = item.AmountFiat
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CountInvoices returns the number of invoices matching the provided filter.
func (s *SQLiteStore) CountInvoices(ctx context.Context, filter InvoiceListFilter) (int, error) {
	query := "SELECT COUNT(*) FROM invoices i"
	clauses := make([]string, 0, 6)
	args := make([]interface{}, 0, 6)
	if status := strings.TrimSpace(filter.Status); status != "" {
		clauses = append(clauses, "i.status = ?")
		args = append(args, status)
	}
	if recipient := strings.TrimSpace(filter.Recipient); recipient != "" {
		clauses = append(clauses, "i.recipient = ?")
		args = append(args, recipient)
	}
	if filter.CreatedFrom != nil {
		clauses = append(clauses, "i.created_at >= ?")
		args = append(args, filter.CreatedFrom.UTC())
	}
	if filter.CreatedTo != nil {
		clauses = append(clauses, "i.created_at <= ?")
		args = append(args, filter.CreatedTo.UTC())
	}
	if filter.UpdatedFrom != nil {
		clauses = append(clauses, "i.updated_at >= ?")
		args = append(args, filter.UpdatedFrom.UTC())
	}
	if filter.UpdatedTo != nil {
		clauses = append(clauses, "i.updated_at <= ?")
		args = append(args, filter.UpdatedTo.UTC())
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// FormatInvoiceView converts an InvoiceView to a JSON-friendly payload.
func FormatInvoiceView(inv InvoiceView) map[string]interface{} {
	payload := map[string]interface{}{
		"invoiceId":          inv.ID,
		"quoteId":            inv.QuoteID,
		"recipient":          inv.Recipient,
		"status":             inv.Status,
		"fiat":               inv.FiatCurrency,
		"token":              inv.Token,
		"mintAsset":          inv.MintAsset,
		"payCurrency":        inv.PayCurrency,
		"amountFiat":         inv.AmountFiat,
		"serviceFeeFiat":     inv.ServiceFeeFiat,
		"totalFiat":          inv.TotalFiat,
		"amountToken":        inv.AmountToken,
		"estimatedPayAmount": inv.EstimatedPayAmount,
		"quoteExpiry":        inv.QuoteExpiry.UTC().Format(time.RFC3339),
		"createdAt":          inv.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt":          inv.UpdatedAt.UTC().Format(time.RFC3339),
		"nowpaymentsId":      inv.NowID,
		"nowpaymentsUrl":     inv.NowURL,
	}
	if inv.TxHash.Valid {
		payload["txHash"] = inv.TxHash.String
	}
	return payload
}

// MarshalInvoiceViews converts reconciliation rows into JSON.
func MarshalInvoiceViews(items []InvoiceView) ([]byte, error) {
	payload := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		payload = append(payload, FormatInvoiceView(item))
	}
	return json.Marshal(payload)
}

// MarshalInvoiceViewCSV renders reconciliation rows as CSV.
func MarshalInvoiceViewCSV(items []InvoiceView) ([]byte, error) {
	var builder strings.Builder
	builder.WriteString("invoice_id,quote_id,recipient,status,fiat,token,mint_asset,pay_currency,amount_fiat,service_fee_fiat,total_fiat,amount_token,estimated_pay_amount,quote_expiry,created_at,updated_at,nowpayments_id,nowpayments_url,tx_hash\n")
	for _, item := range items {
		txHash := ""
		if item.TxHash.Valid {
			txHash = item.TxHash.String
		}
		line := []string{
			csvEscape(item.ID),
			csvEscape(item.QuoteID),
			csvEscape(item.Recipient),
			csvEscape(item.Status),
			csvEscape(item.FiatCurrency),
			csvEscape(item.Token),
			csvEscape(item.MintAsset),
			csvEscape(item.PayCurrency),
			csvEscape(item.AmountFiat),
			csvEscape(item.ServiceFeeFiat),
			csvEscape(item.TotalFiat),
			csvEscape(item.AmountToken),
			csvEscape(item.EstimatedPayAmount),
			csvEscape(item.QuoteExpiry.UTC().Format(time.RFC3339)),
			csvEscape(item.CreatedAt.UTC().Format(time.RFC3339)),
			csvEscape(item.UpdatedAt.UTC().Format(time.RFC3339)),
			csvEscape(item.NowID),
			csvEscape(item.NowURL),
			csvEscape(txHash),
		}
		builder.WriteString(strings.Join(line, ","))
		builder.WriteString("\n")
	}
	return []byte(builder.String()), nil
}

func csvEscape(value string) string {
	if strings.ContainsAny(value, ",\"\n") {
		return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
	}
	return value
}

// InvoiceSummary captures high-level mint reconciliation totals.
type InvoiceSummary struct {
	CountByStatus       map[string]int    `json:"countByStatus"`
	AmountFiatByStatus  map[string]string `json:"amountFiatByStatus"`
	AmountTokenByStatus map[string]string `json:"amountTokenByStatus"`
	TotalInvoices       int               `json:"totalInvoices"`
	MintedInvoices      int               `json:"mintedInvoices"`
	PendingInvoices     int               `json:"pendingInvoices"`
	ErrorInvoices       int               `json:"errorInvoices"`
}

// SummarizeInvoiceViews aggregates reconciliation rows for reporting.
func SummarizeInvoiceViews(items []InvoiceView) (InvoiceSummary, error) {
	summary := InvoiceSummary{
		CountByStatus:       make(map[string]int),
		AmountFiatByStatus:  make(map[string]string),
		AmountTokenByStatus: make(map[string]string),
		TotalInvoices:       len(items),
	}
	fiatTotals := make(map[string]*big.Rat)
	tokenTotals := make(map[string]*big.Rat)
	for _, item := range items {
		status := strings.ToLower(strings.TrimSpace(item.Status))
		if status == "" {
			status = "unknown"
		}
		summary.CountByStatus[status]++
		switch status {
		case "minted":
			summary.MintedInvoices++
		case "pending", "processing":
			summary.PendingInvoices++
		case "error":
			summary.ErrorInvoices++
		}
		if _, ok := fiatTotals[status]; !ok {
			fiatTotals[status] = new(big.Rat)
		}
		if _, ok := tokenTotals[status]; !ok {
			tokenTotals[status] = new(big.Rat)
		}
		fiat, ok := new(big.Rat).SetString(item.AmountFiat)
		if !ok {
			return InvoiceSummary{}, fmt.Errorf("invalid fiat amount %q for invoice %s", item.AmountFiat, item.ID)
		}
		token, ok := new(big.Rat).SetString(item.AmountToken)
		if !ok {
			return InvoiceSummary{}, fmt.Errorf("invalid token amount %q for invoice %s", item.AmountToken, item.ID)
		}
		fiatTotals[status].Add(fiatTotals[status], fiat)
		tokenTotals[status].Add(tokenTotals[status], token)
	}
	for status, total := range fiatTotals {
		summary.AmountFiatByStatus[status] = formatRat(total, 8)
	}
	for status, total := range tokenTotals {
		summary.AmountTokenByStatus[status] = formatRat(total, 8)
	}
	return summary, nil
}

// PaymentRecord captures stored headless (deposit-address) payment metadata,
// the sibling of InvoiceRecord for the checkout-URL-free flow.
type PaymentRecord struct {
	ID           string
	QuoteID      string
	Recipient    string
	Status       string
	NowID        string
	PayCurrency  string
	PayAddress   string
	PayAmount    string
	PayinExtraID string
	TxHash       sql.NullString
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// InsertPayment inserts a new payment row. When p.Status is non-terminal
// (e.g. the "claiming" placeholder resolvePayment starts with, or a filled
// row's real NOWPayments status), idx_payments_active_quote_currency makes
// this an atomic claim of the (p.QuoteID, p.PayCurrency) slot: a concurrent
// caller attempting to insert another non-terminal row for the same pair
// gets ErrPaymentSlotClaimed back instead of a duplicate row ever existing.
func (s *SQLiteStore) InsertPayment(ctx context.Context, p PaymentRecord) error {
	const stmt = `INSERT INTO payments(id, quote_id, recipient, status, nowpayments_id, pay_currency, pay_address, pay_amount, payin_extra_id, tx_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, p.ID, p.QuoteID, p.Recipient, p.Status, p.NowID, p.PayCurrency, p.PayAddress, p.PayAmount, p.PayinExtraID, p.TxHash, p.CreatedAt, p.UpdatedAt)
	if err != nil && isUniqueConstraintErr(err) {
		return ErrPaymentSlotClaimed
	}
	return err
}

// UpdatePayment fills in the real NOWPayments data on a payment row that
// InsertPayment claimed as a placeholder, completing the claim-then-fill
// pattern resolvePayment (server.go) drives. Keyed by id, so it only ever
// touches the exact row the caller claimed -- never another request's.
func (s *SQLiteStore) UpdatePayment(ctx context.Context, p PaymentRecord) error {
	const stmt = `UPDATE payments SET status = ?, nowpayments_id = ?, pay_address = ?, pay_amount = ?, payin_extra_id = ?, updated_at = ? WHERE id = ?`
	_, err := s.db.ExecContext(ctx, stmt, p.Status, p.NowID, p.PayAddress, p.PayAmount, p.PayinExtraID, p.UpdatedAt, p.ID)
	return err
}

// DeletePayment removes a payment row outright. resolvePayment uses this to
// release a claimed (quote_id, pay_currency) slot when the NOWPayments
// CreatePayment call meant to fill it in fails, so a transient upstream
// error can't permanently wedge that slot behind a dead placeholder.
func (s *SQLiteStore) DeletePayment(ctx context.Context, id string) error {
	const stmt = `DELETE FROM payments WHERE id = ?`
	_, err := s.db.ExecContext(ctx, stmt, id)
	return err
}

func (s *SQLiteStore) GetPayment(ctx context.Context, id string) (*PaymentRecord, error) {
	const query = `SELECT id, quote_id, recipient, status, nowpayments_id, pay_currency, pay_address, pay_amount, payin_extra_id, tx_hash, created_at, updated_at FROM payments WHERE id = ?`
	row := s.db.QueryRowContext(ctx, query, id)
	return scanPayment(row)
}

func (s *SQLiteStore) GetPaymentByNowID(ctx context.Context, nowID string) (*PaymentRecord, error) {
	const query = `SELECT id, quote_id, recipient, status, nowpayments_id, pay_currency, pay_address, pay_amount, payin_extra_id, tx_hash, created_at, updated_at FROM payments WHERE nowpayments_id = ?`
	row := s.db.QueryRowContext(ctx, query, nowID)
	return scanPayment(row)
}

// GetLatestPaymentForQuoteCurrency returns the most recently created payment
// attempt for the given quote+currency pair, or nil if none exists. This
// backs the idempotent-reuse check in handlePaymentCreate: a non-terminal
// row here is returned as-is instead of creating a duplicate NOWPayments
// payment for the same intent.
func (s *SQLiteStore) GetLatestPaymentForQuoteCurrency(ctx context.Context, quoteID, payCurrency string) (*PaymentRecord, error) {
	// rowid DESC breaks ties when two attempts share the same created_at
	// (a real possibility: the caller's clock may not have nanosecond
	// resolution, or a caller may substitute a fixed clock in tests) by
	// falling back to insertion order, so "latest" always means the most
	// recently inserted row rather than an arbitrary one among ties.
	const query = `SELECT id, quote_id, recipient, status, nowpayments_id, pay_currency, pay_address, pay_amount, payin_extra_id, tx_hash, created_at, updated_at FROM payments WHERE quote_id = ? AND pay_currency = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`
	row := s.db.QueryRowContext(ctx, query, quoteID, payCurrency)
	return scanPayment(row)
}

func scanPayment(row *sql.Row) (*PaymentRecord, error) {
	var rec PaymentRecord
	err := row.Scan(&rec.ID, &rec.QuoteID, &rec.Recipient, &rec.Status, &rec.NowID, &rec.PayCurrency, &rec.PayAddress, &rec.PayAmount, &rec.PayinExtraID, &rec.TxHash, &rec.CreatedAt, &rec.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// ListPaymentsByStatusOlderThan returns every payment currently in status
// with no update (webhook or otherwise) more recent than cutoff. Backs the
// settlement reconciler's "nothing more is coming" check for partially_paid
// payments: updated_at is bumped on every webhook delivery regardless of
// whether the status string itself changed (see handlePaymentWebhook), so a
// row only matches once NOWPayments has gone quiet on it for the whole
// grace window.
func (s *SQLiteStore) ListPaymentsByStatusOlderThan(ctx context.Context, status string, cutoff time.Time) ([]PaymentRecord, error) {
	const query = `SELECT id, quote_id, recipient, status, nowpayments_id, pay_currency, pay_address, pay_amount, payin_extra_id, tx_hash, created_at, updated_at FROM payments WHERE status = ? AND updated_at < ?`
	rows, err := s.db.QueryContext(ctx, query, status, cutoff.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaymentRecord
	for rows.Next() {
		var rec PaymentRecord
		if err := rows.Scan(&rec.ID, &rec.QuoteID, &rec.Recipient, &rec.Status, &rec.NowID, &rec.PayCurrency, &rec.PayAddress, &rec.PayAmount, &rec.PayinExtraID, &rec.TxHash, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SQLiteStore) UpdatePaymentStatus(ctx context.Context, id, status string, txHash *string) error {
	const stmt = `UPDATE payments SET status = ?, tx_hash = ?, updated_at = ? WHERE id = ?`
	var hash interface{}
	if txHash != nil {
		hash = *txHash
	} else {
		hash = nil
	}
	_, err := s.db.ExecContext(ctx, stmt, status, hash, time.Now().UTC(), id)
	return err
}

// MarshalPayment converts a PaymentRecord into a JSON-friendly payload,
// mirroring MarshalInvoice's shape for the headless-payment sibling route.
func MarshalPayment(p *PaymentRecord) ([]byte, error) {
	if p == nil {
		return json.Marshal(map[string]string{"error": "payment not found"})
	}
	payload := map[string]interface{}{
		"paymentId":     p.ID,
		"quoteId":       p.QuoteID,
		"recipient":     p.Recipient,
		"status":        p.Status,
		"payCurrency":   p.PayCurrency,
		"payAddress":    p.PayAddress,
		"payAmount":     p.PayAmount,
		"payinExtraId":  p.PayinExtraID,
		"nowpaymentsId": p.NowID,
		"createdAt":     p.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt":     p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if p.TxHash.Valid {
		payload["txHash"] = p.TxHash.String
	}
	return json.Marshal(payload)
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
		payload["mintAsset"] = quote.MintAsset
		payload["payCurrency"] = quote.PayCurrency
		payload["serviceFeeFiat"] = quote.ServiceFeeFiat
		payload["totalFiat"] = quote.TotalFiat
		payload["estimatedPayAmount"] = quote.EstimatedPayAmount
	}
	if inv.TxHash.Valid {
		payload["txHash"] = inv.TxHash.String
	}
	return json.Marshal(payload)
}
