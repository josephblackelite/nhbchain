package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"nhbchain/services/payoutd"
)

// MintInvoiceRow captures a reconciliation row from the mint-side payments rail.
type MintInvoiceRow struct {
	InvoiceID          string `json:"invoiceId"`
	QuoteID            string `json:"quoteId"`
	Recipient          string `json:"recipient"`
	Status             string `json:"status"`
	Fiat               string `json:"fiat"`
	Token              string `json:"token"`
	MintAsset          string `json:"mintAsset"`
	PayCurrency        string `json:"payCurrency"`
	AmountFiat         string `json:"amountFiat"`
	ServiceFeeFiat     string `json:"serviceFeeFiat"`
	TotalFiat          string `json:"totalFiat"`
	AmountToken        string `json:"amountToken"`
	EstimatedPayAmount string `json:"estimatedPayAmount"`
	QuoteExpiry        string `json:"quoteExpiry"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
	NowPaymentsID      string `json:"nowpaymentsId"`
	NowPaymentsURL     string `json:"nowpaymentsUrl"`
	TxHash             string `json:"txHash,omitempty"`
}

// MintFilter constrains mint reconciliation queries.
type MintFilter struct {
	Status      string
	Recipient   string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	UpdatedFrom *time.Time
	UpdatedTo   *time.Time
	Limit       int
}

// MintSummary captures aggregate mint-side reconciliation totals.
type MintSummary struct {
	CountByStatus       map[string]int    `json:"countByStatus"`
	AmountFiatByStatus  map[string]string `json:"amountFiatByStatus"`
	AmountTokenByStatus map[string]string `json:"amountTokenByStatus"`
	TotalInvoices       int               `json:"totalInvoices"`
	MintedInvoices      int               `json:"mintedInvoices"`
	PendingInvoices     int               `json:"pendingInvoices"`
	ErrorInvoices       int               `json:"errorInvoices"`
}

// TreasurySummary captures aggregate treasury instruction status.
type TreasurySummary struct {
	CountByStatus map[string]int `json:"countByStatus"`
	CountByAction map[string]int `json:"countByAction"`
	Total         int            `json:"total"`
	Pending       int            `json:"pending"`
	Approved      int            `json:"approved"`
	Rejected      int            `json:"rejected"`
}

// OpsSummary captures the cross-rail operator overview.
type OpsSummary struct {
	GeneratedAt string          `json:"generatedAt"`
	Mint        MintSummary     `json:"mint"`
	Merchant    MerchantSummary `json:"merchant"`
	Treasury    TreasurySummary `json:"treasury"`
	Payout      PayoutSummary   `json:"payout"`
}

// MintReader exposes read-only access to the payments gateway store.
type MintReader struct {
	db *sql.DB
}

// NewMintReader opens the payments gateway database.
func NewMintReader(path string) (*MintReader, error) {
	db, err := sql.Open("sqlite", strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	return &MintReader{db: db}, nil
}

// Close releases the underlying database.
func (r *MintReader) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// List returns mint-side reconciliation rows.
func (r *MintReader) List(ctx context.Context, filter MintFilter) ([]MintInvoiceRow, error) {
	query := `
SELECT i.id, i.quote_id, i.recipient, i.status, i.nowpayments_id, i.nowpayments_url, i.tx_hash, i.created_at, i.updated_at,
       q.fiat_currency, q.token, q.mint_asset, q.pay_currency, q.amount_fiat, q.service_fee_fiat, q.total_fiat, q.amount_token, q.estimated_pay_amount, q.expiry
FROM invoices i
JOIN quotes q ON q.id = i.quote_id
`
	clauses := make([]string, 0, 6)
	args := make([]interface{}, 0, 7)
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
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]MintInvoiceRow, 0)
	for rows.Next() {
		var item MintInvoiceRow
		var quoteExpiry, createdAt, updatedAt time.Time
		var txHash sql.NullString
		if err := rows.Scan(
			&item.InvoiceID,
			&item.QuoteID,
			&item.Recipient,
			&item.Status,
			&item.NowPaymentsID,
			&item.NowPaymentsURL,
			&txHash,
			&createdAt,
			&updatedAt,
			&item.Fiat,
			&item.Token,
			&item.MintAsset,
			&item.PayCurrency,
			&item.AmountFiat,
			&item.ServiceFeeFiat,
			&item.TotalFiat,
			&item.AmountToken,
			&item.EstimatedPayAmount,
			&quoteExpiry,
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
		item.QuoteExpiry = quoteExpiry.UTC().Format(time.RFC3339)
		item.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		if txHash.Valid {
			item.TxHash = txHash.String
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Summarize aggregates mint reconciliation totals.
func (r *MintReader) Summarize(ctx context.Context, filter MintFilter) (MintSummary, error) {
	filter.Limit = 0
	items, err := r.List(ctx, filter)
	if err != nil {
		return MintSummary{}, err
	}
	summary := MintSummary{
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
			return MintSummary{}, fmt.Errorf("invalid amountFiat %q for invoice %s", item.AmountFiat, item.InvoiceID)
		}
		token, ok := new(big.Rat).SetString(item.AmountToken)
		if !ok {
			return MintSummary{}, fmt.Errorf("invalid amountToken %q for invoice %s", item.AmountToken, item.InvoiceID)
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

// TreasuryFilter constrains treasury instruction queries.
type TreasuryFilter struct {
	Status string
	Action string
	Asset  string
	Limit  int
}

// TreasuryReader exposes read-only treasury instruction queries.
type TreasuryReader struct {
	store payoutd.TreasuryInstructionStore
}

// NewTreasuryReader opens the treasury instruction store.
func NewTreasuryReader(path string) (*TreasuryReader, error) {
	store, err := payoutd.NewBoltTreasuryInstructionStore(path)
	if err != nil {
		return nil, err
	}
	return &TreasuryReader{store: store}, nil
}

// Close releases the underlying store.
func (r *TreasuryReader) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

// List returns filtered treasury instructions.
func (r *TreasuryReader) List(filter TreasuryFilter) ([]payoutd.TreasuryInstruction, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("treasury reader not initialised")
	}
	items, err := r.store.List()
	if err != nil {
		return nil, err
	}
	filtered := make([]payoutd.TreasuryInstruction, 0, len(items))
	for _, item := range items {
		if status := strings.TrimSpace(filter.Status); status != "" && !strings.EqualFold(string(item.Status), status) {
			continue
		}
		if action := strings.TrimSpace(filter.Action); action != "" && !strings.EqualFold(item.Action, action) {
			continue
		}
		if asset := strings.TrimSpace(filter.Asset); asset != "" && !strings.EqualFold(item.Asset, asset) {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].CreatedAt.After(filtered[j].CreatedAt) })
	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}
	return filtered, nil
}

// Summarize aggregates treasury instruction counts.
func (r *TreasuryReader) Summarize(filter TreasuryFilter) (TreasurySummary, error) {
	items, err := r.List(filter)
	if err != nil {
		return TreasurySummary{}, err
	}
	summary := TreasurySummary{
		CountByStatus: make(map[string]int),
		CountByAction: make(map[string]int),
		Total:         len(items),
	}
	for _, item := range items {
		status := strings.ToLower(strings.TrimSpace(string(item.Status)))
		action := strings.ToLower(strings.TrimSpace(item.Action))
		summary.CountByStatus[status]++
		summary.CountByAction[action]++
		switch status {
		case "pending":
			summary.Pending++
		case "approved":
			summary.Approved++
		case "rejected":
			summary.Rejected++
		}
	}
	return summary, nil
}

// MerchantTradeRow captures a merchant-facing trade settlement row.
type MerchantTradeRow struct {
	TradeID       string `json:"tradeId"`
	OfferID       string `json:"offerId"`
	Buyer         string `json:"buyer"`
	Seller        string `json:"seller"`
	BaseToken     string `json:"baseToken"`
	BaseAmount    string `json:"baseAmount"`
	QuoteToken    string `json:"quoteToken"`
	QuoteAmount   string `json:"quoteAmount"`
	EscrowBaseID  string `json:"escrowBaseId"`
	EscrowQuoteID string `json:"escrowQuoteId"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

// MerchantFilter constrains merchant trade reporting.
type MerchantFilter struct {
	Status string
	Seller string
	Buyer  string
	Limit  int
}

// MerchantSummary aggregates trade lifecycle counts for operators.
type MerchantSummary struct {
	CountByStatus  map[string]int `json:"countByStatus"`
	TotalTrades    int            `json:"totalTrades"`
	SettledTrades  int            `json:"settledTrades"`
	OpenTrades     int            `json:"openTrades"`
	DisputedTrades int            `json:"disputedTrades"`
}

// MerchantReader exposes read-only access to escrow-gateway trade records.
type MerchantReader struct {
	db *sql.DB
}

// NewMerchantReader opens the escrow-gateway database.
func NewMerchantReader(path string) (*MerchantReader, error) {
	db, err := sql.Open("sqlite", strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	return &MerchantReader{db: db}, nil
}

// Close releases the underlying database.
func (r *MerchantReader) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// List returns merchant trade rows.
func (r *MerchantReader) List(ctx context.Context, filter MerchantFilter) ([]MerchantTradeRow, error) {
	query := `
SELECT id, offer_id, buyer, seller, base_token, base_amount, quote_token, quote_amount, escrow_base_id, escrow_quote_id, status, created_at, updated_at
FROM p2p_trades`
	clauses := make([]string, 0, 3)
	args := make([]interface{}, 0, 4)
	if status := strings.TrimSpace(filter.Status); status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, status)
	}
	if seller := strings.TrimSpace(filter.Seller); seller != "" {
		clauses = append(clauses, "seller = ?")
		args = append(args, seller)
	}
	if buyer := strings.TrimSpace(filter.Buyer); buyer != "" {
		clauses = append(clauses, "buyer = ?")
		args = append(args, buyer)
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY updated_at DESC, created_at DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]MerchantTradeRow, 0)
	for rows.Next() {
		var item MerchantTradeRow
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&item.TradeID, &item.OfferID, &item.Buyer, &item.Seller, &item.BaseToken, &item.BaseAmount, &item.QuoteToken, &item.QuoteAmount, &item.EscrowBaseID, &item.EscrowQuoteID, &item.Status, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		item.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		items = append(items, item)
	}
	return items, rows.Err()
}

// Summarize aggregates merchant trade counts.
func (r *MerchantReader) Summarize(ctx context.Context, filter MerchantFilter) (MerchantSummary, error) {
	filter.Limit = 0
	items, err := r.List(ctx, filter)
	if err != nil {
		return MerchantSummary{}, err
	}
	summary := MerchantSummary{
		CountByStatus: make(map[string]int),
		TotalTrades:   len(items),
	}
	for _, item := range items {
		status := strings.ToLower(strings.TrimSpace(item.Status))
		if status == "" {
			status = "unknown"
		}
		summary.CountByStatus[status]++
		switch status {
		case "settled", "resolved":
			summary.SettledTrades++
		case "disputed":
			summary.DisputedTrades++
			summary.OpenTrades++
		case "created", "partial_funded", "funded":
			summary.OpenTrades++
		}
	}
	return summary, nil
}

// PayoutFilter constrains payout execution reporting.
type PayoutFilter struct {
	Status string
	Asset  string
	Limit  int
}

// PayoutSummary aggregates payout execution counts.
type PayoutSummary struct {
	CountByStatus   map[string]int `json:"countByStatus"`
	CountByAsset    map[string]int `json:"countByAsset"`
	TotalExecutions int            `json:"totalExecutions"`
	Settled         int            `json:"settled"`
	Failed          int            `json:"failed"`
	Aborted         int            `json:"aborted"`
	Processing      int            `json:"processing"`
}

// PayoutExecutionReader exposes read-only payout execution reporting.
type PayoutExecutionReader struct {
	store payoutd.PayoutExecutionStore
}

// NewPayoutExecutionReader opens the payout execution store.
func NewPayoutExecutionReader(path string) (*PayoutExecutionReader, error) {
	store, err := payoutd.NewBoltPayoutExecutionStore(path)
	if err != nil {
		return nil, err
	}
	return &PayoutExecutionReader{store: store}, nil
}

// Close releases the underlying store.
func (r *PayoutExecutionReader) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

// List returns filtered payout executions.
func (r *PayoutExecutionReader) List(filter PayoutFilter) ([]payoutd.PayoutExecution, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("payout reader not initialised")
	}
	items, err := r.store.List()
	if err != nil {
		return nil, err
	}
	filtered := make([]payoutd.PayoutExecution, 0, len(items))
	for _, item := range items {
		if status := strings.TrimSpace(filter.Status); status != "" && !strings.EqualFold(string(item.Status), status) {
			continue
		}
		if asset := strings.TrimSpace(filter.Asset); asset != "" && !strings.EqualFold(item.StableAsset, asset) {
			continue
		}
		filtered = append(filtered, item)
		if filter.Limit > 0 && len(filtered) >= filter.Limit {
			break
		}
	}
	return filtered, nil
}

// Summarize aggregates payout execution counts.
func (r *PayoutExecutionReader) Summarize(filter PayoutFilter) (PayoutSummary, error) {
	items, err := r.List(filter)
	if err != nil {
		return PayoutSummary{}, err
	}
	summary := PayoutSummary{
		CountByStatus:   make(map[string]int),
		CountByAsset:    make(map[string]int),
		TotalExecutions: len(items),
	}
	for _, item := range items {
		status := strings.ToLower(strings.TrimSpace(string(item.Status)))
		asset := strings.ToUpper(strings.TrimSpace(item.StableAsset))
		summary.CountByStatus[status]++
		if asset != "" {
			summary.CountByAsset[asset]++
		}
		switch status {
		case "settled":
			summary.Settled++
		case "failed":
			summary.Failed++
		case "aborted":
			summary.Aborted++
		case "processing":
			summary.Processing++
		}
	}
	return summary, nil
}

// BuildOpsSummary aggregates mint and treasury views into one operator summary.
func BuildOpsSummary(ctx context.Context, mint *MintReader, merchant *MerchantReader, treasury *TreasuryReader, payout *PayoutExecutionReader, now time.Time) (OpsSummary, error) {
	summary := OpsSummary{GeneratedAt: now.UTC().Format(time.RFC3339)}
	if mint != nil {
		mintSummary, err := mint.Summarize(ctx, MintFilter{})
		if err != nil {
			return OpsSummary{}, err
		}
		summary.Mint = mintSummary
	}
	if merchant != nil {
		merchantSummary, err := merchant.Summarize(ctx, MerchantFilter{})
		if err != nil {
			return OpsSummary{}, err
		}
		summary.Merchant = merchantSummary
	}
	if treasury != nil {
		treasurySummary, err := treasury.Summarize(TreasuryFilter{})
		if err != nil {
			return OpsSummary{}, err
		}
		summary.Treasury = treasurySummary
	}
	if payout != nil {
		payoutSummary, err := payout.Summarize(PayoutFilter{})
		if err != nil {
			return OpsSummary{}, err
		}
		summary.Payout = payoutSummary
	}
	return summary, nil
}

func formatRat(r *big.Rat, precision int) string {
	f := new(big.Float).SetRat(r)
	f = f.SetPrec(uint(precision * 4))
	text := f.Text('f', precision)
	text = strings.TrimRight(text, "0")
	text = strings.TrimRight(text, ".")
	if text == "" {
		text = "0"
	}
	return text
}

func marshalJSON(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}
