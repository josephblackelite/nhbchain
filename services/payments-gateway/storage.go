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
