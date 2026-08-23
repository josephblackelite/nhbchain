package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhbchain/core"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore("file:test-payments?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	return store
}

type stubNowPayments struct {
	createFn    func(ctx context.Context, req *NowPaymentsInvoiceRequest) (*NowPaymentsInvoice, error)
	getFn       func(ctx context.Context, id string) (*NowPaymentsInvoice, error)
	estimateFn  func(ctx context.Context, req *NowPaymentsEstimateRequest) (*NowPaymentsEstimate, error)
	createCalls int

	createPaymentFn    func(ctx context.Context, req *NowPaymentsPaymentRequest) (*NowPaymentsPayment, error)
	getPaymentFn       func(ctx context.Context, id string) (*NowPaymentsPayment, error)
	listCoinsFn        func(ctx context.Context) ([]string, error)
	createPaymentCalls int
	lastPaymentReq     *NowPaymentsPaymentRequest

	// mu guards createCalls/createPaymentCalls/lastPaymentReq so concurrent
	// callers (see TestPaymentCreateConcurrentRequestsClaimOnlyOnce) don't
	// race on this stub's own bookkeeping -- independent of whatever
	// concurrency guarantee the code under test does or doesn't provide.
	mu sync.Mutex
}

func (s *stubNowPayments) CreateInvoice(ctx context.Context, req *NowPaymentsInvoiceRequest) (*NowPaymentsInvoice, error) {
	s.mu.Lock()
	s.createCalls++
	s.mu.Unlock()
	if s.createFn == nil {
		return &NowPaymentsInvoice{}, nil
	}
	return s.createFn(ctx, req)
}

func (s *stubNowPayments) GetInvoice(ctx context.Context, id string) (*NowPaymentsInvoice, error) {
	if s.getFn == nil {
		return &NowPaymentsInvoice{InvoiceID: id}, nil
	}
	return s.getFn(ctx, id)
}

func (s *stubNowPayments) Estimate(ctx context.Context, req *NowPaymentsEstimateRequest) (*NowPaymentsEstimate, error) {
	if s.estimateFn == nil {
		return &NowPaymentsEstimate{
			CurrencyFrom:    req.CurrencyFrom,
			CurrencyTo:      req.CurrencyTo,
			EstimatedAmount: flexibleAmount(req.Amount),
			AmountTo:        flexibleAmount(req.Amount),
		}, nil
	}
	return s.estimateFn(ctx, req)
}

func (s *stubNowPayments) CreatePayment(ctx context.Context, req *NowPaymentsPaymentRequest) (*NowPaymentsPayment, error) {
	s.mu.Lock()
	s.createPaymentCalls++
	s.lastPaymentReq = req
	s.mu.Unlock()
	if s.createPaymentFn == nil {
		return &NowPaymentsPayment{
			PaymentID:     flexibleAmount("np-payment-" + req.OrderID),
			PayAddress:    "addr-" + req.PayCurrency,
			PayAmount:     flexibleAmount(req.PriceAmount),
			PayCurrency:   req.PayCurrency,
			PaymentStatus: "waiting",
		}, nil
	}
	return s.createPaymentFn(ctx, req)
}

func (s *stubNowPayments) GetPayment(ctx context.Context, id string) (*NowPaymentsPayment, error) {
	if s.getPaymentFn == nil {
		return &NowPaymentsPayment{PaymentID: flexibleAmount(id)}, nil
	}
	return s.getPaymentFn(ctx, id)
}

func (s *stubNowPayments) ListMerchantCoins(ctx context.Context) ([]string, error) {
	if s.listCoinsFn == nil {
		return []string{"btc", "eth", "usdttrc20"}, nil
	}
	return s.listCoinsFn(ctx)
}

type stubNode struct {
	lastVoucher   core.MintVoucher
	lastSignature string
	txHash        string
	callCount     int
	err           error
}

func (n *stubNode) MintWithSig(ctx context.Context, voucher core.MintVoucher, signature string) (string, error) {
	n.callCount++
	n.lastVoucher = voucher
	n.lastSignature = signature
	if n.err != nil {
		return "", n.err
	}
	if n.txHash == "" {
		n.txHash = "0xdeadbeef"
	}
	return n.txHash, nil
}

type stubSigner struct {
	payloads [][]byte
	sig      []byte
	err      error
}

func (s *stubSigner) Address() string { return "nhb1test" }

func (s *stubSigner) Sign(ctx context.Context, payload []byte) ([]byte, error) {
	s.payloads = append(s.payloads, payload)
	if s.err != nil {
		return nil, s.err
	}
	if len(s.sig) == 0 {
		return bytes.Repeat([]byte{0x01}, 65), nil
	}
	return s.sig, nil
}

func newTestServer(t *testing.T, store *SQLiteStore, np *stubNowPayments, node *stubNode, signer *stubSigner) *Server {
	oracle := NewOracle(time.Minute, 0.10, 0.50)
	srv := NewServer(store, oracle, np, node, signer, time.Minute, "USD", "NHB", 0, "secret", "https://api.nhbcoin.com/webhooks/nowpayments")
	fixed := time.Date(2024, 12, 1, 10, 0, 0, 0, time.UTC)
	srv.nowFn = func() time.Time { return fixed }
	return srv
}

func TestQuoteCalculation(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)
	reqBody := []byte(`{"fiat":"USD","mintAsset":"NHB","payCurrency":"BTC","amountMint":"125.00"}`)
	req := httptest.NewRequest(http.MethodPost, "/quotes", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp QuoteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AmountToken != "125" {
		t.Fatalf("expected 125 tokens, got %s", resp.AmountToken)
	}
	if resp.MintAsset != "NHB" || resp.PayCurrency != "BTC" {
		t.Fatalf("unexpected asset split in response: %+v", resp)
	}
	if resp.EstimatedPayAmount != "125" && resp.EstimatedPayAmount != "125.00" {
		t.Fatalf("unexpected estimated pay amount: %s", resp.EstimatedPayAmount)
	}
	if resp.Expiry != srv.nowFn().Add(time.Minute).Format(time.RFC3339) {
		t.Fatalf("unexpected expiry: %s", resp.Expiry)
	}
	if resp.Fiat != "USD" || resp.Token != "NHB" {
		t.Fatalf("unexpected currencies in response: %+v", resp)
	}
}

func TestQuoteRejectsZNHB(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{
		createFn: func(ctx context.Context, req *NowPaymentsInvoiceRequest) (*NowPaymentsInvoice, error) {
			t.Fatal("NOWPayments invoice must never be created for a ZNHB quote request")
			return nil, nil
		},
	}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)
	reqBody := []byte(`{"fiat":"USD","mintAsset":"ZNHB","payCurrency":"BTC","amountFiat":"100.00"}`)
	req := httptest.NewRequest(http.MethodPost, "/quotes", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a ZNHB quote request, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("fixed supply")) {
		t.Fatalf("expected a fixed-supply error message, got: %s", w.Body.String())
	}
}

func TestInvoiceIdempotency(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	var storedQuote QuoteResponse
	np := &stubNowPayments{
		createFn: func(ctx context.Context, req *NowPaymentsInvoiceRequest) (*NowPaymentsInvoice, error) {
			return &NowPaymentsInvoice{InvoiceID: "np-1", InvoiceURL: "https://nowpay/invoice/np-1"}, nil
		},
		getFn: func(ctx context.Context, id string) (*NowPaymentsInvoice, error) {
			return &NowPaymentsInvoice{InvoiceID: id, PaymentStatus: "finished"}, nil
		},
	}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)
	quoteReq := httptest.NewRequest(http.MethodPost, "/quotes", bytes.NewReader([]byte(`{"fiat":"USD","mintAsset":"NHB","payCurrency":"USDT","amountMint":"50"}`)))
	quoteResp := httptest.NewRecorder()
	srv.ServeHTTP(quoteResp, quoteReq)
	if quoteResp.Code != http.StatusOK {
		t.Fatalf("quote creation failed: %s", quoteResp.Body.String())
	}
	if err := json.Unmarshal(quoteResp.Body.Bytes(), &storedQuote); err != nil {
		t.Fatalf("decode quote response: %v", err)
	}

	invoicePayload := []byte(`{"quoteId":"` + storedQuote.QuoteID + `","recipient":"nhb1alice"}`)
	req := httptest.NewRequest(http.MethodPost, "/invoices", bytes.NewReader(invoicePayload))
	req.Header.Set(headerIdempotencyKey, "abc123")
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("invoice create failed: %s", res.Body.String())
	}
	first := res.Body.Bytes()

	// replay with same key
	req2 := httptest.NewRequest(http.MethodPost, "/invoices", bytes.NewReader(invoicePayload))
	req2.Header.Set(headerIdempotencyKey, "abc123")
	res2 := httptest.NewRecorder()
	srv.ServeHTTP(res2, req2)
	if res2.Code != http.StatusOK {
		t.Fatalf("second invoice create failed: %s", res2.Body.String())
	}
	if !bytes.Equal(first, res2.Body.Bytes()) {
		t.Fatalf("responses differ for idempotent request")
	}
	if np.createCalls != 1 {
		t.Fatalf("expected single invoice creation, got %d", np.createCalls)
	}
}

func TestWebhookReconciliationAndMint(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)
	// create quote
	quoteReq := httptest.NewRequest(http.MethodPost, "/quotes", bytes.NewReader([]byte(`{"fiat":"USD","mintAsset":"NHB","payCurrency":"USDC","amountMint":"20"}`)))
	quoteRes := httptest.NewRecorder()
	srv.ServeHTTP(quoteRes, quoteReq)
	if quoteRes.Code != http.StatusOK {
		t.Fatalf("quote failed: %s", quoteRes.Body.String())
	}
	var quote QuoteResponse
	if err := json.Unmarshal(quoteRes.Body.Bytes(), &quote); err != nil {
		t.Fatalf("decode quote: %v", err)
	}

	npID := "np-xyz"
	np.createFn = func(ctx context.Context, req *NowPaymentsInvoiceRequest) (*NowPaymentsInvoice, error) {
		return &NowPaymentsInvoice{InvoiceID: npID, InvoiceURL: "https://nowpay/invoice/" + npID}, nil
	}
	np.getFn = func(ctx context.Context, id string) (*NowPaymentsInvoice, error) {
		return &NowPaymentsInvoice{InvoiceID: id, PaymentStatus: "finished"}, nil
	}

	invoicePayload := []byte(`{"quoteId":"` + quote.QuoteID + `","recipient":"nhb1bob"}`)
	invReq := httptest.NewRequest(http.MethodPost, "/invoices", bytes.NewReader(invoicePayload))
	invReq.Header.Set(headerIdempotencyKey, "idem-1")
	invRes := httptest.NewRecorder()
	srv.ServeHTTP(invRes, invReq)
	if invRes.Code != http.StatusOK {
		t.Fatalf("invoice create failed: %s", invRes.Body.String())
	}
	var invResp map[string]string
	if err := json.Unmarshal(invRes.Body.Bytes(), &invResp); err != nil {
		t.Fatalf("decode invoice resp: %v", err)
	}
	invoiceID := invResp["invoiceId"]
	if invoiceID == "" {
		t.Fatalf("missing invoice id in response")
	}

	webhook := NowPaymentsWebhookPayload{InvoiceID: npID, PaymentStatus: "finished"}
	body, _ := json.Marshal(webhook)
	sig := computeTestHMAC("secret", body)

	whReq := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader(body))
	whReq.Header.Set(headerNowPaymentsSig, sig)
	whRes := httptest.NewRecorder()
	srv.ServeHTTP(whRes, whReq)
	if whRes.Code != http.StatusOK {
		t.Fatalf("webhook failed: %s", whRes.Body.String())
	}
	var mintResp map[string]string
	if err := json.Unmarshal(whRes.Body.Bytes(), &mintResp); err != nil {
		t.Fatalf("decode webhook resp: %v", err)
	}
	if mintResp["voucherHash"] == "" {
		t.Fatalf("expected voucherHash in response")
	}
	if node.callCount != 1 {
		t.Fatalf("expected node mint call")
	}
	if node.lastVoucher.Recipient != "nhb1bob" || node.lastVoucher.Token != "NHB" {
		t.Fatalf("unexpected voucher: %+v", node.lastVoucher)
	}
	if node.lastVoucher.InvoiceID != invoiceID {
		t.Fatalf("invoice mismatch: %s", node.lastVoucher.InvoiceID)
	}
	// mintWithVoucher scales the human-readable quote.AmountToken ("20") to
	// the 18-decimal wei-integer MintVoucher.Amount actually requires (see
	// docs/otc/voucher.md and mintWithVoucher's doc comment) -- the voucher
	// amount is never the same string as quote.AmountToken itself.
	wantAmount := "20000000000000000000"
	if node.lastVoucher.Amount != wantAmount {
		t.Fatalf("amount mismatch: got %s want %s (quote.AmountToken=%s)", node.lastVoucher.Amount, wantAmount, quote.AmountToken)
	}
	if node.lastVoucher.ChainID != core.MintChainID {
		t.Fatalf("unexpected chain id: %d", node.lastVoucher.ChainID)
	}
	if node.lastSignature == "" {
		t.Fatalf("expected signature to be provided")
	}
	inv, err := store.GetInvoice(context.Background(), invoiceID)
	if err != nil {
		t.Fatalf("fetch invoice: %v", err)
	}
	if inv.Status != "minted" {
		t.Fatalf("expected invoice minted, got %s", inv.Status)
	}
	if !inv.TxHash.Valid {
		t.Fatalf("expected tx hash to be recorded")
	}
	events, err := store.ListWebhookEvents(context.Background(), WebhookEventFilter{})
	if err != nil {
		t.Fatalf("list webhook events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 webhook event, got %d", len(events))
	}
	if !events[0].SignatureVerified {
		t.Fatalf("expected signature_verified=true, got %+v", events[0])
	}
	if events[0].EventType != "invoice" || events[0].InvoiceID != npID {
		t.Fatalf("unexpected webhook event: %+v", events[0])
	}
}

// createTestQuote is a small helper shared by the headless-payment tests
// below: it posts /swap/quotes and decodes the response.
func createTestQuote(t *testing.T, srv *Server, body string) QuoteResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/quotes", bytes.NewReader([]byte(body)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("quote creation failed: %s", w.Body.String())
	}
	var quote QuoteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &quote); err != nil {
		t.Fatalf("decode quote: %v", err)
	}
	return quote
}

func createTestPayment(t *testing.T, srv *Server, quoteID, payCurrency, recipient, idemKey string) map[string]string {
	t.Helper()
	payload := []byte(`{"quoteId":"` + quoteID + `","payCurrency":"` + payCurrency + `","recipient":"` + recipient + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/swap/payments", bytes.NewReader(payload))
	req.Header.Set(headerIdempotencyKey, idemKey)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("payment create failed: %s", w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode payment response: %v", err)
	}
	return resp
}

// TestPaymentCreateIdempotentReuseAndFreshAttempts covers all three branches
// of the reuse policy described in handlePaymentCreate: a second request for
// the same quote+currency reuses the outstanding payment untouched; a
// request for a different currency always creates a fresh one; and once the
// prior attempt reaches a terminal status, the same currency gets a fresh
// payment again rather than reusing the dead one.
func TestPaymentCreateIdempotentReuseAndFreshAttempts(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)

	quote := createTestQuote(t, srv, `{"fiat":"USD","mintAsset":"NHB","payCurrency":"BTC","amountMint":"10"}`)

	first := createTestPayment(t, srv, quote.QuoteID, "BTC", "nhb1alice", "pay-key-1")
	if np.createPaymentCalls != 1 {
		t.Fatalf("expected 1 nowpayments payment creation, got %d", np.createPaymentCalls)
	}

	// Same quote, same currency, different idempotency key -> must reuse the
	// existing outstanding (non-terminal) payment rather than create a
	// second one.
	second := createTestPayment(t, srv, quote.QuoteID, "BTC", "nhb1alice", "pay-key-2")
	if np.createPaymentCalls != 1 {
		t.Fatalf("expected reuse to avoid a second nowpayments call, got %d calls", np.createPaymentCalls)
	}
	if first["paymentId"] != second["paymentId"] {
		t.Fatalf("expected reused payment id, got %s vs %s", first["paymentId"], second["paymentId"])
	}

	// Different currency on the same quote -> always a fresh payment.
	third := createTestPayment(t, srv, quote.QuoteID, "ETH", "nhb1alice", "pay-key-3")
	if np.createPaymentCalls != 2 {
		t.Fatalf("expected a fresh nowpayments call for a different currency, got %d calls", np.createPaymentCalls)
	}
	if third["paymentId"] == first["paymentId"] {
		t.Fatalf("expected a distinct payment id for a different currency")
	}

	// Once the BTC attempt is terminal, requesting BTC again must create a
	// fresh payment rather than reusing the dead one.
	if err := store.UpdatePaymentStatus(context.Background(), first["paymentId"], "failed", nil); err != nil {
		t.Fatalf("mark payment failed: %v", err)
	}
	fourth := createTestPayment(t, srv, quote.QuoteID, "BTC", "nhb1alice", "pay-key-4")
	if np.createPaymentCalls != 3 {
		t.Fatalf("expected a fresh nowpayments call after a terminal prior attempt, got %d calls", np.createPaymentCalls)
	}
	if fourth["paymentId"] == first["paymentId"] {
		t.Fatalf("expected a distinct payment id after the prior attempt went terminal")
	}
}

// TestPaymentCreateConcurrentRequestsClaimOnlyOnce is the regression test for
// the payment-creation TOCTOU: the old SELECT-for-existing-then-
// CreatePayment-then-INSERT sequence in handlePaymentCreate left a window
// where two near-simultaneous requests for the same (quote_id, pay_currency)
// could both pass the SELECT before either INSERT committed, each creating
// its own live NOWPayments deposit address. Real goroutines (not sequential
// calls) drive genuinely concurrent requests here, each with its own
// idempotency key -- mirroring the exact failure mode described in the bug
// report, where a caller mints a fresh key on every call and so can't rely
// on the idempotency-key mechanism alone. Run with `go test -race`: the fix
// must be clean under the race detector, not just functionally correct.
func TestPaymentCreateConcurrentRequestsClaimOnlyOnce(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })

	// release holds every goroutine that reaches the (stubbed) NOWPayments
	// CreatePayment call until they're all let go together, so the test
	// actually witnesses concurrent requests contending for the claim --
	// rather than the first request finishing (and releasing the claim)
	// before the second even starts, which an in-memory SQLite DB is fast
	// enough to let happen by accident otherwise. entered counts how many
	// concurrent calls actually got past the claim and into NOWPayments: in
	// the fixed implementation this must be exactly 1, no matter how many
	// requests race for the slot, because every other request fails its own
	// claim attempt immediately and never calls NOWPayments at all.
	release := make(chan struct{})
	var mu sync.Mutex
	entered := 0
	np := &stubNowPayments{}
	np.createPaymentFn = func(ctx context.Context, req *NowPaymentsPaymentRequest) (*NowPaymentsPayment, error) {
		mu.Lock()
		entered++
		mu.Unlock()
		<-release
		return &NowPaymentsPayment{
			PaymentID:     flexibleAmount("np-payment-" + req.OrderID),
			PayAddress:    "addr-" + req.PayCurrency,
			PayAmount:     flexibleAmount(req.PriceAmount),
			PayCurrency:   req.PayCurrency,
			PaymentStatus: "waiting",
		}, nil
	}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)

	quote := createTestQuote(t, srv, `{"fiat":"USD","mintAsset":"NHB","payCurrency":"BTC","amountMint":"10"}`)

	const concurrency = 8
	var wg sync.WaitGroup
	codes := make([]int, concurrency)
	responses := make([]map[string]string, concurrency)
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(i int) {
			defer wg.Done()
			payload := []byte(`{"quoteId":"` + quote.QuoteID + `","payCurrency":"BTC","recipient":"nhb1alice"}`)
			req := httptest.NewRequest(http.MethodPost, "/swap/payments", bytes.NewReader(payload))
			// A distinct idempotency key per request -- the idempotency-key
			// table must not be what prevents the double-create here; only
			// the (quote_id, pay_currency) claim may.
			req.Header.Set(headerIdempotencyKey, fmt.Sprintf("concurrent-key-%d", i))
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			codes[i] = w.Code
			var resp map[string]string
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			responses[i] = resp
		}(i)
	}

	// Best-effort widen of the race window: give every goroutine a chance to
	// reach (and block in) CreatePayment before releasing them together.
	// Not required for correctness -- only one should ever get there in the
	// fixed implementation regardless of scheduling -- just for making the
	// test meaningfully exercise the wait-for-the-winner path below.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	gotEntered := entered
	mu.Unlock()
	if gotEntered != 1 {
		t.Fatalf("expected exactly 1 concurrent NOWPayments CreatePayment call to be in flight, got %d", gotEntered)
	}
	if np.createPaymentCalls != 1 {
		t.Fatalf("expected exactly 1 NOWPayments CreatePayment call total, got %d", np.createPaymentCalls)
	}

	var paymentID string
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("request %d failed: %d", i, code)
		}
		if responses[i]["paymentId"] == "" {
			t.Fatalf("request %d missing paymentId: %+v", i, responses[i])
		}
		if paymentID == "" {
			paymentID = responses[i]["paymentId"]
		} else if responses[i]["paymentId"] != paymentID {
			t.Fatalf("expected every concurrent request to be given the same reused payment id, got %s vs %s", paymentID, responses[i]["paymentId"])
		}
		if responses[i]["payAddress"] == "" {
			t.Fatalf("request %d got an unfilled placeholder instead of the completed reused payment: %+v", i, responses[i])
		}
		// fillClaimedPayment lowercases payCurrency before it ever reaches
		// NowPaymentsPaymentRequest (NOWPayments' API itself expects
		// lowercase currency codes), so the stub's echoed "addr-"+PayCurrency
		// comes back lowercase too.
		if responses[i]["payAddress"] != "addr-btc" {
			t.Fatalf("request %d got an unexpected payAddress: %+v", i, responses[i])
		}
	}

	// Exactly one row must exist in storage for this quote+currency -- no
	// duplicate live payment was ever persisted.
	final, err := store.GetLatestPaymentForQuoteCurrency(context.Background(), quote.QuoteID, "BTC")
	if err != nil {
		t.Fatalf("fetch final payment: %v", err)
	}
	if final == nil || final.ID != paymentID {
		t.Fatalf("expected the sole persisted payment to match the reused id, got %+v", final)
	}
	if final.PayAddress != "addr-btc" || final.Status != "waiting" {
		t.Fatalf("expected the persisted payment to be fully filled in, got %+v", final)
	}
}

// TestPaymentCreateRejectsDisabledCurrency proves the server re-validates the
// requested pay currency against NOWPayments' enabled-coins list rather than
// trusting client input alone.
func TestPaymentCreateRejectsDisabledCurrency(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{listCoinsFn: func(ctx context.Context) ([]string, error) {
		return []string{"btc"}, nil
	}}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)

	quote := createTestQuote(t, srv, `{"fiat":"USD","mintAsset":"NHB","payCurrency":"XRP","amountMint":"10"}`)
	payload := []byte(`{"quoteId":"` + quote.QuoteID + `","payCurrency":"XRP","recipient":"nhb1alice"}`)
	req := httptest.NewRequest(http.MethodPost, "/swap/payments", bytes.NewReader(payload))
	req.Header.Set(headerIdempotencyKey, "disabled-1")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a disabled currency, got %d: %s", w.Code, w.Body.String())
	}
	if np.createPaymentCalls != 0 {
		t.Fatalf("must never create a nowpayments payment for a rejected currency")
	}
}

// TestPaymentStatusRouteReturnsTrackedRecord covers GET /swap/payments/{id}.
func TestPaymentStatusRouteReturnsTrackedRecord(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{createPaymentFn: func(ctx context.Context, req *NowPaymentsPaymentRequest) (*NowPaymentsPayment, error) {
		return &NowPaymentsPayment{
			PaymentID:     flexibleAmount("np-status-1"),
			PayAddress:    "bc1qexample",
			PayAmount:     flexibleAmount("0.00123"),
			PayCurrency:   req.PayCurrency,
			PayinExtraID:  "",
			PaymentStatus: "waiting",
		}, nil
	}}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)

	quote := createTestQuote(t, srv, `{"fiat":"USD","mintAsset":"NHB","payCurrency":"BTC","amountMint":"10"}`)
	created := createTestPayment(t, srv, quote.QuoteID, "BTC", "nhb1carol", "status-1")

	getReq := httptest.NewRequest(http.MethodGet, "/swap/payments/"+created["paymentId"], nil)
	getRes := httptest.NewRecorder()
	srv.ServeHTTP(getRes, getReq)
	if getRes.Code != http.StatusOK {
		t.Fatalf("payment get failed: %s", getRes.Body.String())
	}
	var status map[string]interface{}
	if err := json.Unmarshal(getRes.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode payment status: %v", err)
	}
	if status["payAddress"] != "bc1qexample" {
		t.Fatalf("unexpected payAddress: %+v", status)
	}
	if status["payAmount"] != "0.00123" {
		t.Fatalf("unexpected payAmount: %+v", status)
	}
	if status["payCurrency"] != "BTC" {
		t.Fatalf("unexpected payCurrency: %+v", status)
	}
	if status["status"] != "waiting" {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status["quoteId"] != quote.QuoteID {
		t.Fatalf("unexpected quoteId: %+v", status)
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/swap/payments/does-not-exist", nil)
	missingRes := httptest.NewRecorder()
	srv.ServeHTTP(missingRes, missingReq)
	if missingRes.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown payment, got %d", missingRes.Code)
	}
}

// TestPaymentWebhookMintsOnFinishedAndNoOpsOnDuplicate covers the headless
// sibling of TestWebhookReconciliationAndMint: a 'finished' payment-shaped
// IPN mints via the exact same mintWithVoucher mechanism, and a duplicate
// delivery of the same IPN is a no-op that never mints twice.
func TestPaymentWebhookMintsOnFinishedAndNoOpsOnDuplicate(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	npID := "np-payment-xyz"
	np := &stubNowPayments{
		createPaymentFn: func(ctx context.Context, req *NowPaymentsPaymentRequest) (*NowPaymentsPayment, error) {
			return &NowPaymentsPayment{
				PaymentID:     flexibleAmount(npID),
				PayAddress:    "bc1qexample",
				PayAmount:     flexibleAmount("0.001"),
				PayCurrency:   req.PayCurrency,
				PaymentStatus: "waiting",
			}, nil
		},
		getPaymentFn: func(ctx context.Context, id string) (*NowPaymentsPayment, error) {
			// OutcomeAmount deliberately differs from the quoted 15 NHB --
			// settlement now mints whatever actually netted to us (see
			// settlePayment), not the originally quoted amountToken, so a
			// real test of that behavior needs the two to diverge.
			return &NowPaymentsPayment{
				PaymentID:     flexibleAmount(id),
				PaymentStatus: "finished",
				OutcomeAmount: flexibleAmount("14.5"),
			}, nil
		},
	}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)

	quote := createTestQuote(t, srv, `{"fiat":"USD","mintAsset":"NHB","payCurrency":"BTC","amountMint":"15"}`)
	created := createTestPayment(t, srv, quote.QuoteID, "BTC", "nhb1dave", "webhook-1")

	webhook := NowPaymentsWebhookPayload{PaymentID: flexibleAmount(npID), OrderID: created["paymentId"], PaymentStatus: "finished"}
	body, _ := json.Marshal(webhook)
	sig := computeTestHMAC("secret", body)

	whReq := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader(body))
	whReq.Header.Set(headerNowPaymentsSig, sig)
	whRes := httptest.NewRecorder()
	srv.ServeHTTP(whRes, whReq)
	if whRes.Code != http.StatusOK {
		t.Fatalf("webhook failed: %s", whRes.Body.String())
	}
	var mintResp map[string]string
	if err := json.Unmarshal(whRes.Body.Bytes(), &mintResp); err != nil {
		t.Fatalf("decode webhook resp: %v", err)
	}
	if mintResp["status"] != "minted" || mintResp["txHash"] == "" {
		t.Fatalf("expected a minted response, got %+v", mintResp)
	}
	if node.callCount != 1 {
		t.Fatalf("expected exactly one mint call, got %d", node.callCount)
	}
	// Mint amount tracks OutcomeAmount ("14.5"), not the originally quoted
	// amountToken ("15") -- confirms settlement is outcome-driven. The
	// on-chain voucher amount is wei-scaled (see mintWithVoucher); 14.5 * 1e18.
	wantWeiAmount := "14500000000000000000"
	if node.lastVoucher.Recipient != "nhb1dave" || node.lastVoucher.Amount != wantWeiAmount {
		t.Fatalf("unexpected voucher: %+v (want amount %s)", node.lastVoucher, wantWeiAmount)
	}
	if node.lastVoucher.Amount == quote.AmountToken {
		t.Fatalf("expected mint amount to diverge from quote.AmountToken (%s), got the same value", quote.AmountToken)
	}
	if mintResp["mintAmount"] != "14.5" {
		t.Fatalf("expected mintAmount 14.5 in webhook response, got %+v", mintResp)
	}

	// Duplicate delivery of the same IPN must not mint a second time.
	whReq2 := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader(body))
	whReq2.Header.Set(headerNowPaymentsSig, sig)
	whRes2 := httptest.NewRecorder()
	srv.ServeHTTP(whRes2, whReq2)
	if whRes2.Code != http.StatusOK {
		t.Fatalf("duplicate webhook failed: %s", whRes2.Body.String())
	}
	var dupResp map[string]string
	if err := json.Unmarshal(whRes2.Body.Bytes(), &dupResp); err != nil {
		t.Fatalf("decode duplicate webhook resp: %v", err)
	}
	if dupResp["status"] != "already minted" {
		t.Fatalf("expected already-minted no-op, got %+v", dupResp)
	}
	if node.callCount != 1 {
		t.Fatalf("expected mint call count to stay at 1 after duplicate webhook, got %d", node.callCount)
	}

	payment, err := store.GetPayment(context.Background(), created["paymentId"])
	if err != nil {
		t.Fatalf("fetch payment: %v", err)
	}
	if payment.Status != "minted" || !payment.TxHash.Valid {
		t.Fatalf("expected payment marked minted with a tx hash, got %+v", payment)
	}

	// Both the original and the duplicate delivery are recorded as their own
	// webhook_events rows -- the audit log tracks every attempt, not just
	// the ones that actually changed state.
	events, err := store.ListWebhookEvents(context.Background(), WebhookEventFilter{})
	if err != nil {
		t.Fatalf("list webhook events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 webhook events (original + duplicate), got %d", len(events))
	}
	for _, evt := range events {
		if !evt.SignatureVerified || evt.EventType != "payment" || evt.PaymentID != npID {
			t.Fatalf("unexpected webhook event: %+v", evt)
		}
	}
}

// TestCurrenciesRouteOrdersPreferredFirst covers GET /swap/currencies.
func TestCurrenciesRouteOrdersPreferredFirst(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{listCoinsFn: func(ctx context.Context) ([]string, error) {
		return []string{"DOGE", "btc", "xmr", "eth", "btc"}, nil
	}}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)

	req := httptest.NewRequest(http.MethodGet, "/swap/currencies", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("currencies route failed: %s", w.Body.String())
	}
	var resp struct {
		Currencies []string `json:"currencies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode currencies: %v", err)
	}
	if len(resp.Currencies) != 4 {
		t.Fatalf("expected deduped currency list of length 4, got %v", resp.Currencies)
	}
	if resp.Currencies[0] != "btc" || resp.Currencies[1] != "eth" {
		t.Fatalf("expected preferred currencies (btc, eth) first, got %v", resp.Currencies)
	}
}

func computeTestHMAC(secret string, body []byte) string {
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookSignatureFailure(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)
	webhook := NowPaymentsWebhookPayload{InvoiceID: "np-1"}
	body, _ := json.Marshal(webhook)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader(body))
	req.Header.Set(headerNowPaymentsSig, "bad-signature")
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.Code)
	}
	events, err := store.ListWebhookEvents(context.Background(), WebhookEventFilter{})
	if err != nil {
		t.Fatalf("list webhook events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 webhook event recorded even on signature failure, got %d", len(events))
	}
	if events[0].SignatureVerified {
		t.Fatalf("expected signature_verified=false, got %+v", events[0])
	}
	if events[0].EventType != "invoice" || events[0].InvoiceID != "np-1" {
		t.Fatalf("expected best-effort id extraction despite invalid signature, got %+v", events[0])
	}
}

func TestAdminWebhookEventsListRequiresAuth(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)
	srv.SetAdminToken("admin-secret")

	req := httptest.NewRequest(http.MethodGet, "/admin/webhook-events", nil)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no Authorization header, got %d", res.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/admin/webhook-events", nil)
	req2.Header.Set("Authorization", "Bearer wrong-token")
	res2 := httptest.NewRecorder()
	srv.ServeHTTP(res2, req2)
	if res2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with a wrong bearer token, got %d", res2.Code)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/admin/webhook-events", nil)
	req3.Header.Set("Authorization", "Bearer admin-secret")
	res3 := httptest.NewRecorder()
	srv.ServeHTTP(res3, req3)
	if res3.Code != http.StatusOK {
		t.Fatalf("expected 200 with the correct bearer token, got %d: %s", res3.Code, res3.Body.String())
	}
}

func TestAdminWebhookEventsListUnsetTokenFailsClosed(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)
	// No SetAdminToken call: the route must stay unauthorized rather than
	// falling open with an empty-token comparison.

	req := httptest.NewRequest(http.MethodGet, "/admin/webhook-events", nil)
	req.Header.Set("Authorization", "Bearer ")
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when no admin token is configured, got %d", res.Code)
	}
}

func TestAdminWebhookEventsListFilterAndPagination(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)
	srv.SetAdminToken("admin-secret")

	// One verified "invoice" event, one signature-failed "unrecognized" event.
	verifiedBody, _ := json.Marshal(NowPaymentsWebhookPayload{InvoiceID: "inv-filter-1", PaymentStatus: "finished"})
	verifiedReq := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader(verifiedBody))
	verifiedReq.Header.Set(headerNowPaymentsSig, computeTestHMAC("secret", verifiedBody))
	srv.ServeHTTP(httptest.NewRecorder(), verifiedReq)

	failedBody := []byte(`not-json`)
	failedReq := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader(failedBody))
	failedReq.Header.Set(headerNowPaymentsSig, "bad-signature")
	srv.ServeHTTP(httptest.NewRecorder(), failedReq)

	authed := func(req *http.Request) *httptest.ResponseRecorder {
		req.Header.Set("Authorization", "Bearer admin-secret")
		res := httptest.NewRecorder()
		srv.ServeHTTP(res, req)
		return res
	}

	// Unfiltered list returns both, newest first.
	listRes := authed(httptest.NewRequest(http.MethodGet, "/admin/webhook-events", nil))
	if listRes.Code != http.StatusOK {
		t.Fatalf("list failed: %s", listRes.Body.String())
	}
	var listed struct {
		Total      int                      `json:"total"`
		Items      []map[string]interface{} `json:"items"`
		NextCursor interface{}               `json:"nextCursor"`
	}
	if err := json.Unmarshal(listRes.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.Total != 2 || len(listed.Items) != 2 {
		t.Fatalf("expected 2 total events, got %+v", listed)
	}
	if listed.Items[0]["eventType"] != "unrecognized" || listed.Items[1]["eventType"] != "invoice" {
		t.Fatalf("expected newest-first ordering, got %+v", listed.Items)
	}
	if listed.NextCursor != nil {
		t.Fatalf("expected nil nextCursor when page is smaller than the limit, got %v", listed.NextCursor)
	}

	// Filter by signatureVerified=true isolates the invoice event.
	filteredRes := authed(httptest.NewRequest(http.MethodGet, "/admin/webhook-events?signatureVerified=true", nil))
	if filteredRes.Code != http.StatusOK {
		t.Fatalf("filtered list failed: %s", filteredRes.Body.String())
	}
	var filtered struct {
		Total int                      `json:"total"`
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(filteredRes.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered list: %v", err)
	}
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0]["invoiceId"] != "inv-filter-1" {
		t.Fatalf("expected signatureVerified filter to isolate the invoice event, got %+v", filtered)
	}

	// limit=1 pages via beforeId: first page returns the newest row plus a
	// cursor; the second page (beforeId=<that row's id>) returns the older row.
	page1 := authed(httptest.NewRequest(http.MethodGet, "/admin/webhook-events?limit=1", nil))
	var page1Body struct {
		Items      []map[string]interface{} `json:"items"`
		NextCursor float64                  `json:"nextCursor"`
	}
	if err := json.Unmarshal(page1.Body.Bytes(), &page1Body); err != nil {
		t.Fatalf("decode page1: %v", err)
	}
	if len(page1Body.Items) != 1 || page1Body.Items[0]["eventType"] != "unrecognized" {
		t.Fatalf("unexpected page1: %+v", page1Body)
	}
	if page1Body.NextCursor == 0 {
		t.Fatalf("expected a non-zero nextCursor on a full page")
	}

	page2URL := fmt.Sprintf("/admin/webhook-events?limit=1&beforeId=%d", int64(page1Body.NextCursor))
	page2 := authed(httptest.NewRequest(http.MethodGet, page2URL, nil))
	var page2Body struct {
		Items      []map[string]interface{} `json:"items"`
		NextCursor interface{}              `json:"nextCursor"`
	}
	if err := json.Unmarshal(page2.Body.Bytes(), &page2Body); err != nil {
		t.Fatalf("decode page2: %v", err)
	}
	if len(page2Body.Items) != 1 || page2Body.Items[0]["eventType"] != "invoice" {
		t.Fatalf("unexpected page2: %+v", page2Body)
	}
	if page2Body.NextCursor != nil {
		t.Fatalf("expected nil nextCursor once the last page is reached, got %v", page2Body.NextCursor)
	}
}

func TestInvoiceListSummaryAndExport(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	np := &stubNowPayments{}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)
	quoteReq := httptest.NewRequest(http.MethodPost, "/quotes", bytes.NewReader([]byte(`{"fiat":"USD","mintAsset":"NHB","payCurrency":"BTC","amountMint":"35"}`)))
	quoteRes := httptest.NewRecorder()
	srv.ServeHTTP(quoteRes, quoteReq)
	if quoteRes.Code != http.StatusOK {
		t.Fatalf("quote failed: %s", quoteRes.Body.String())
	}
	var quote QuoteResponse
	if err := json.Unmarshal(quoteRes.Body.Bytes(), &quote); err != nil {
		t.Fatalf("decode quote: %v", err)
	}

	npID := "np-report-1"
	np.createFn = func(ctx context.Context, req *NowPaymentsInvoiceRequest) (*NowPaymentsInvoice, error) {
		return &NowPaymentsInvoice{InvoiceID: npID, InvoiceURL: "https://nowpay/invoice/" + npID}, nil
	}
	np.getFn = func(ctx context.Context, id string) (*NowPaymentsInvoice, error) {
		return &NowPaymentsInvoice{InvoiceID: id, PaymentStatus: "finished"}, nil
	}
	invoicePayload := []byte(`{"quoteId":"` + quote.QuoteID + `","recipient":"nhb1report"}`)
	invReq := httptest.NewRequest(http.MethodPost, "/invoices", bytes.NewReader(invoicePayload))
	invReq.Header.Set(headerIdempotencyKey, "report-1")
	invRes := httptest.NewRecorder()
	srv.ServeHTTP(invRes, invReq)
	if invRes.Code != http.StatusOK {
		t.Fatalf("invoice create failed: %s", invRes.Body.String())
	}

	webhook := NowPaymentsWebhookPayload{InvoiceID: npID, PaymentStatus: "finished"}
	body, _ := json.Marshal(webhook)
	whReq := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader(body))
	whReq.Header.Set(headerNowPaymentsSig, computeTestHMAC("secret", body))
	whRes := httptest.NewRecorder()
	srv.ServeHTTP(whRes, whReq)
	if whRes.Code != http.StatusOK {
		t.Fatalf("webhook failed: %s", whRes.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/invoices?status=minted&recipient=nhb1report", nil)
	listRes := httptest.NewRecorder()
	srv.ServeHTTP(listRes, listReq)
	if listRes.Code != http.StatusOK {
		t.Fatalf("invoice list failed: %s", listRes.Body.String())
	}
	var listed struct {
		Total int                      `json:"total"`
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(listRes.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.Total != 1 || len(listed.Items) != 1 {
		t.Fatalf("unexpected list payload %+v", listed)
	}
	if listed.Items[0]["status"] != "minted" {
		t.Fatalf("expected minted invoice, got %+v", listed.Items[0])
	}

	summaryReq := httptest.NewRequest(http.MethodGet, "/reconciliation/summary", nil)
	summaryRes := httptest.NewRecorder()
	srv.ServeHTTP(summaryRes, summaryReq)
	if summaryRes.Code != http.StatusOK {
		t.Fatalf("summary failed: %s", summaryRes.Body.String())
	}
	var summary InvoiceSummary
	if err := json.Unmarshal(summaryRes.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.MintedInvoices != 1 || summary.TotalInvoices != 1 {
		t.Fatalf("unexpected summary %+v", summary)
	}
	if summary.AmountFiatByStatus["minted"] != "35" {
		t.Fatalf("unexpected fiat summary %+v", summary.AmountFiatByStatus)
	}

	exportReq := httptest.NewRequest(http.MethodGet, "/reconciliation/export?format=csv", nil)
	exportRes := httptest.NewRecorder()
	srv.ServeHTTP(exportRes, exportReq)
	if exportRes.Code != http.StatusOK {
		t.Fatalf("export failed: %s", exportRes.Body.String())
	}
	if got := exportRes.Header().Get("Content-Type"); !strings.Contains(got, "text/csv") {
		t.Fatalf("expected csv content type, got %q", got)
	}
	csvBody, err := io.ReadAll(exportRes.Body)
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if !strings.Contains(string(csvBody), "invoice_id,quote_id,recipient") {
		t.Fatalf("missing csv header: %s", string(csvBody))
	}
	if !strings.Contains(string(csvBody), "nhb1report") {
		t.Fatalf("missing invoice row: %s", string(csvBody))
	}
}
