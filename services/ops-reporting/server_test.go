package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"nhbchain/services/payoutd"
)

func TestServerHealthzAndAuth(t *testing.T) {
	server, cleanup := newTestOpsServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected healthz 200, got %d", res.Code)
	}
	if body := strings.TrimSpace(res.Body.String()); body != "ok" {
		t.Fatalf("expected healthz body ok, got %q", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/summary", nil)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized summary, got %d", res.Code)
	}
}

func TestServerSummaryAndExports(t *testing.T) {
	server, cleanup := newTestOpsServer(t)
	defer cleanup()

	summaryReq := authorizedRequest(http.MethodGet, "/summary")
	summaryRes := httptest.NewRecorder()
	server.ServeHTTP(summaryRes, summaryReq)
	if summaryRes.Code != http.StatusOK {
		t.Fatalf("summary failed: %s", summaryRes.Body.String())
	}
	var summary OpsSummary
	if err := json.Unmarshal(summaryRes.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.Mint.TotalInvoices != 2 || summary.Mint.MintedInvoices != 1 || summary.Mint.PendingInvoices != 1 {
		t.Fatalf("unexpected mint summary: %+v", summary.Mint)
	}
	if summary.Merchant.TotalTrades != 2 || summary.Merchant.SettledTrades != 1 || summary.Merchant.OpenTrades != 1 {
		t.Fatalf("unexpected merchant summary: %+v", summary.Merchant)
	}
	if summary.Treasury.Total != 2 || summary.Treasury.Pending != 1 || summary.Treasury.Approved != 1 {
		t.Fatalf("unexpected treasury summary: %+v", summary.Treasury)
	}
	if summary.Payout.TotalExecutions != 2 || summary.Payout.Settled != 1 || summary.Payout.Failed != 1 {
		t.Fatalf("unexpected payout summary: %+v", summary.Payout)
	}

	mintReq := authorizedRequest(http.MethodGet, "/mint/invoices?status=minted&recipient=nhb1merchant")
	mintRes := httptest.NewRecorder()
	server.ServeHTTP(mintRes, mintReq)
	if mintRes.Code != http.StatusOK {
		t.Fatalf("mint invoice list failed: %s", mintRes.Body.String())
	}
	var mintItems []MintInvoiceRow
	if err := json.Unmarshal(mintRes.Body.Bytes(), &mintItems); err != nil {
		t.Fatalf("decode mint items: %v", err)
	}
	if len(mintItems) != 1 || mintItems[0].Status != "minted" || mintItems[0].Recipient != "nhb1merchant" {
		t.Fatalf("unexpected mint items: %+v", mintItems)
	}

	mintExportReq := authorizedRequest(http.MethodGet, "/mint/export?format=csv")
	mintExportRes := httptest.NewRecorder()
	server.ServeHTTP(mintExportRes, mintExportReq)
	if mintExportRes.Code != http.StatusOK {
		t.Fatalf("mint export failed: %s", mintExportRes.Body.String())
	}
	if contentType := mintExportRes.Header().Get("Content-Type"); !strings.Contains(contentType, "text/csv") {
		t.Fatalf("expected csv content type, got %q", contentType)
	}
	mintCSV := mintExportRes.Body.String()
	if !strings.Contains(mintCSV, "invoice_id,quote_id,recipient") || !strings.Contains(mintCSV, "nhb1merchant") {
		t.Fatalf("unexpected mint csv: %s", mintCSV)
	}

	merchantReq := authorizedRequest(http.MethodGet, "/merchant/trades?status=settled")
	merchantRes := httptest.NewRecorder()
	server.ServeHTTP(merchantRes, merchantReq)
	if merchantRes.Code != http.StatusOK {
		t.Fatalf("merchant list failed: %s", merchantRes.Body.String())
	}
	var merchantItems []MerchantTradeRow
	if err := json.Unmarshal(merchantRes.Body.Bytes(), &merchantItems); err != nil {
		t.Fatalf("decode merchant items: %v", err)
	}
	if len(merchantItems) != 1 || merchantItems[0].TradeID != "trade-1" {
		t.Fatalf("unexpected merchant items: %+v", merchantItems)
	}

	merchantExportReq := authorizedRequest(http.MethodGet, "/merchant/export?format=csv")
	merchantExportRes := httptest.NewRecorder()
	server.ServeHTTP(merchantExportRes, merchantExportReq)
	if merchantExportRes.Code != http.StatusOK {
		t.Fatalf("merchant export failed: %s", merchantExportRes.Body.String())
	}
	if merchantCSV := merchantExportRes.Body.String(); !strings.Contains(merchantCSV, "trade_id,offer_id,buyer") || !strings.Contains(merchantCSV, "trade-1") {
		t.Fatalf("unexpected merchant csv: %s", merchantCSV)
	}

	treasuryReq := authorizedRequest(http.MethodGet, "/treasury/instructions?status=pending")
	treasuryRes := httptest.NewRecorder()
	server.ServeHTTP(treasuryRes, treasuryReq)
	if treasuryRes.Code != http.StatusOK {
		t.Fatalf("treasury list failed: %s", treasuryRes.Body.String())
	}
	var treasuryItems []payoutd.TreasuryInstruction
	if err := json.Unmarshal(treasuryRes.Body.Bytes(), &treasuryItems); err != nil {
		t.Fatalf("decode treasury items: %v", err)
	}
	if len(treasuryItems) != 1 || treasuryItems[0].Status != payoutd.TreasuryInstructionPending {
		t.Fatalf("unexpected treasury items: %+v", treasuryItems)
	}

	treasuryExportReq := authorizedRequest(http.MethodGet, "/treasury/export?format=csv")
	treasuryExportRes := httptest.NewRecorder()
	server.ServeHTTP(treasuryExportRes, treasuryExportReq)
	if treasuryExportRes.Code != http.StatusOK {
		t.Fatalf("treasury export failed: %s", treasuryExportRes.Body.String())
	}
	treasuryCSV := treasuryExportRes.Body.String()
	if !strings.Contains(treasuryCSV, "id,action,asset,amount") || !strings.Contains(treasuryCSV, "treasury-pending") {
		t.Fatalf("unexpected treasury csv: %s", treasuryCSV)
	}

	payoutReq := authorizedRequest(http.MethodGet, "/payout/executions?status=settled")
	payoutRes := httptest.NewRecorder()
	server.ServeHTTP(payoutRes, payoutReq)
	if payoutRes.Code != http.StatusOK {
		t.Fatalf("payout list failed: %s", payoutRes.Body.String())
	}
	var payoutItems []payoutd.PayoutExecution
	if err := json.Unmarshal(payoutRes.Body.Bytes(), &payoutItems); err != nil {
		t.Fatalf("decode payout items: %v", err)
	}
	if len(payoutItems) != 1 || payoutItems[0].IntentID != "intent-1" {
		t.Fatalf("unexpected payout items: %+v", payoutItems)
	}

	payoutExportReq := authorizedRequest(http.MethodGet, "/payout/export?format=csv")
	payoutExportRes := httptest.NewRecorder()
	server.ServeHTTP(payoutExportRes, payoutExportReq)
	if payoutExportRes.Code != http.StatusOK {
		t.Fatalf("payout export failed: %s", payoutExportRes.Body.String())
	}
	if payoutCSV := payoutExportRes.Body.String(); !strings.Contains(payoutCSV, "intent_id,stable_asset,stable_amount") || !strings.Contains(payoutCSV, "intent-1") {
		t.Fatalf("unexpected payout csv: %s", payoutCSV)
	}
}

func TestServerValidation(t *testing.T) {
	server, cleanup := newTestOpsServer(t)
	defer cleanup()

	req := authorizedRequest(http.MethodGet, "/mint/invoices?limit=bad")
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for mint limit, got %d", res.Code)
	}

	req = authorizedRequest(http.MethodGet, "/treasury/instructions?limit=-1")
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for treasury limit, got %d", res.Code)
	}

	req = authorizedRequest(http.MethodGet, "/merchant/trades?limit=bad")
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for merchant limit, got %d", res.Code)
	}

	req = authorizedRequest(http.MethodGet, "/payout/executions?limit=-1")
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for payout limit, got %d", res.Code)
	}
}

func newTestOpsServer(t *testing.T) (*Server, func()) {
	t.Helper()

	dir := t.TempDir()
	paymentsDB := filepath.Join(dir, "payments.db")
	merchantDB := filepath.Join(dir, "merchant.db")
	treasuryDB := filepath.Join(dir, "treasury.db")
	payoutDB := filepath.Join(dir, "payout.db")

	if err := seedPaymentsDB(paymentsDB); err != nil {
		t.Fatalf("seed payments db: %v", err)
	}
	if err := seedMerchantDB(merchantDB); err != nil {
		t.Fatalf("seed merchant db: %v", err)
	}
	store, err := payoutd.NewBoltTreasuryInstructionStore(treasuryDB)
	if err != nil {
		t.Fatalf("open treasury store: %v", err)
	}
	if err := seedTreasuryStore(store); err != nil {
		_ = store.Close()
		t.Fatalf("seed treasury store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close treasury store: %v", err)
	}
	payoutStore, err := payoutd.NewBoltPayoutExecutionStore(payoutDB)
	if err != nil {
		t.Fatalf("open payout store: %v", err)
	}
	if err := seedPayoutStore(payoutStore); err != nil {
		_ = payoutStore.Close()
		t.Fatalf("seed payout store: %v", err)
	}
	if err := payoutStore.Close(); err != nil {
		t.Fatalf("close payout store: %v", err)
	}

	mintReader, err := NewMintReader(paymentsDB)
	if err != nil {
		t.Fatalf("new mint reader: %v", err)
	}
	merchantReader, err := NewMerchantReader(merchantDB)
	if err != nil {
		_ = mintReader.Close()
		t.Fatalf("new merchant reader: %v", err)
	}
	treasuryReader, err := NewTreasuryReader(treasuryDB)
	if err != nil {
		_ = mintReader.Close()
		_ = merchantReader.Close()
		t.Fatalf("new treasury reader: %v", err)
	}
	payoutReader, err := NewPayoutExecutionReader(payoutDB)
	if err != nil {
		_ = mintReader.Close()
		_ = merchantReader.Close()
		_ = treasuryReader.Close()
		t.Fatalf("new payout reader: %v", err)
	}
	server := NewServer(mintReader, merchantReader, treasuryReader, payoutReader, "ops-secret")
	server.nowFn = func() time.Time {
		return time.Date(2026, 4, 12, 10, 30, 0, 0, time.UTC)
	}
	cleanup := func() {
		_ = payoutReader.Close()
		_ = treasuryReader.Close()
		_ = merchantReader.Close()
		_ = mintReader.Close()
		_ = os.Remove(paymentsDB)
		_ = os.Remove(merchantDB)
		_ = os.Remove(treasuryDB)
		_ = os.Remove(payoutDB)
	}
	return server, cleanup
}

func authorizedRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Authorization", "Bearer ops-secret")
	return req
}

func seedPaymentsDB(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS quotes (
			id TEXT PRIMARY KEY,
			fiat_currency TEXT NOT NULL,
			token TEXT NOT NULL,
			mint_asset TEXT NOT NULL DEFAULT '',
			pay_currency TEXT NOT NULL DEFAULT '',
			amount_fiat TEXT NOT NULL,
			service_fee_fiat TEXT NOT NULL DEFAULT '0',
			total_fiat TEXT NOT NULL DEFAULT '0',
			amount_token TEXT NOT NULL,
			estimated_pay_amount TEXT NOT NULL DEFAULT '',
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
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	base := time.Date(2026, 4, 12, 9, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO quotes(id, fiat_currency, token, mint_asset, pay_currency, amount_fiat, service_fee_fiat, total_fiat, amount_token, estimated_pay_amount, expiry, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"quote-1", "USD", "NHB", "NHB", "BTC", "50", "1", "51", "50", "0.0005", base.Add(15*time.Minute), base,
	); err != nil {
		return err
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO quotes(id, fiat_currency, token, mint_asset, pay_currency, amount_fiat, service_fee_fiat, total_fiat, amount_token, estimated_pay_amount, expiry, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"quote-2", "USD", "NHB", "NHB", "USDT", "20", "0", "20", "20", "20", base.Add(20*time.Minute), base.Add(2*time.Minute),
	); err != nil {
		return err
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO invoices(id, quote_id, recipient, status, nowpayments_id, nowpayments_url, tx_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"invoice-1", "quote-1", "nhb1merchant", "minted", "np-1", "https://nowpayments.example/invoice/np-1", "0xabc123", base.Add(time.Minute), base.Add(3*time.Minute),
	); err != nil {
		return err
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO invoices(id, quote_id, recipient, status, nowpayments_id, nowpayments_url, tx_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"invoice-2", "quote-2", "nhb1buyer", "pending", "np-2", "https://nowpayments.example/invoice/np-2", nil, base.Add(2*time.Minute), base.Add(4*time.Minute),
	); err != nil {
		return err
	}
	return nil
}

func seedTreasuryStore(store *payoutd.BoltTreasuryInstructionStore) error {
	base := time.Date(2026, 4, 12, 8, 0, 0, 0, time.UTC)
	approvedAt := base.Add(20 * time.Minute)
	items := []payoutd.TreasuryInstruction{
		{
			ID:          "treasury-pending",
			Action:      "refill",
			Asset:       "USDT",
			Amount:      "1000",
			Source:      "cold-usdt",
			Destination: "hot-usdt",
			Status:      payoutd.TreasuryInstructionPending,
			RequestedBy: "ops-maker",
			Notes:       "top up hot wallet",
			CreatedAt:   base.Add(30 * time.Minute),
		},
		{
			ID:          "treasury-approved",
			Action:      "sweep",
			Asset:       "USDC",
			Amount:      "500",
			Source:      "hot-usdc",
			Destination: "cold-usdc",
			Status:      payoutd.TreasuryInstructionApproved,
			RequestedBy: "ops-maker",
			ApprovedBy:  "ops-checker",
			Notes:       "reduce hot exposure",
			ReviewNotes: "approved",
			CreatedAt:   base,
			ApprovedAt:  &approvedAt,
		},
	}
	for _, item := range items {
		if err := store.Put(item); err != nil {
			return err
		}
	}
	return nil
}

func seedMerchantDB(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	stmts := []string{
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
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	base := time.Date(2026, 4, 12, 9, 30, 0, 0, time.UTC)
	items := []struct {
		id, offerID, buyer, seller, baseToken, baseAmount, quoteToken, quoteAmount, escrowBaseID, escrowQuoteID, status string
		createdAt, updatedAt                                                                                            time.Time
	}{
		{"trade-1", "offer-1", "nhb1buyer", "nhb1merchant", "NHB", "100", "USDT", "100", "escrow-base-1", "escrow-quote-1", "settled", base, base.Add(10 * time.Minute)},
		{"trade-2", "offer-2", "nhb1buyer2", "nhb1merchant2", "NHB", "75", "USDC", "75", "escrow-base-2", "escrow-quote-2", "funded", base.Add(2 * time.Minute), base.Add(5 * time.Minute)},
	}
	for _, item := range items {
		if _, err := db.ExecContext(context.Background(),
			`INSERT INTO p2p_trades(id, offer_id, buyer, seller, base_token, base_amount, quote_token, quote_amount, escrow_base_id, escrow_quote_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.id, item.offerID, item.buyer, item.seller, item.baseToken, item.baseAmount, item.quoteToken, item.quoteAmount, item.escrowBaseID, item.escrowQuoteID, item.status, item.createdAt, item.updatedAt,
		); err != nil {
			return err
		}
	}
	return nil
}

func seedPayoutStore(store *payoutd.BoltPayoutExecutionStore) error {
	base := time.Date(2026, 4, 12, 11, 0, 0, 0, time.UTC)
	settledAt := base.Add(5 * time.Minute)
	items := []payoutd.PayoutExecution{
		{
			IntentID:     "intent-1",
			StableAsset:  "USDC",
			StableAmount: "100",
			NhbAmount:    "100",
			Destination:  "0xaaa",
			TxHash:       "0xtx1",
			Status:       payoutd.PayoutExecutionSettled,
			CreatedAt:    base,
			UpdatedAt:    settledAt,
			SettledAt:    &settledAt,
		},
		{
			IntentID:     "intent-2",
			StableAsset:  "USDT",
			StableAmount: "50",
			NhbAmount:    "50",
			Destination:  "0xbbb",
			Status:       payoutd.PayoutExecutionFailed,
			Error:        "insufficient hot wallet balance",
			CreatedAt:    base.Add(2 * time.Minute),
			UpdatedAt:    base.Add(3 * time.Minute),
		},
	}
	for _, item := range items {
		if err := store.Put(item); err != nil {
			return err
		}
	}
	return nil
}
