package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	createCalls int
}

func (s *stubNowPayments) CreateInvoice(ctx context.Context, req *NowPaymentsInvoiceRequest) (*NowPaymentsInvoice, error) {
	s.createCalls++
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
	srv := NewServer(store, oracle, np, node, signer, time.Minute, "USD", "secret")
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
	srv.oracle.Update("NHB", "feed-a", 5.0, srv.nowFn())

	reqBody := []byte(`{"fiat":"USD","token":"NHB","amountFiat":"125.00"}`)
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
	if resp.AmountToken != "25" {
		t.Fatalf("expected 25 tokens, got %s", resp.AmountToken)
	}
	if resp.Expiry != srv.nowFn().Add(time.Minute).Format(time.RFC3339) {
		t.Fatalf("unexpected expiry: %s", resp.Expiry)
	}
	if resp.Fiat != "USD" || resp.Token != "NHB" {
		t.Fatalf("unexpected currencies in response: %+v", resp)
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
	srv.oracle.Update("NHB", "feed-a", 5.0, srv.nowFn())

	quoteReq := httptest.NewRequest(http.MethodPost, "/quotes", bytes.NewReader([]byte(`{"fiat":"USD","token":"NHB","amountFiat":"50"}`)))
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
	srv.oracle.Update("NHB", "feed-a", 5.0, srv.nowFn())

	// create quote
	quoteReq := httptest.NewRequest(http.MethodPost, "/quotes", bytes.NewReader([]byte(`{"fiat":"USD","token":"NHB","amountFiat":"20"}`)))
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
	if node.callCount != 1 {
		t.Fatalf("expected node mint call")
	}
	if node.lastVoucher.Recipient != "nhb1bob" || node.lastVoucher.Token != "NHB" {
		t.Fatalf("unexpected voucher: %+v", node.lastVoucher)
	}
	if node.lastVoucher.InvoiceID != invoiceID {
		t.Fatalf("invoice mismatch: %s", node.lastVoucher.InvoiceID)
	}
	if node.lastVoucher.Amount != quote.AmountToken {
		t.Fatalf("amount mismatch: got %s want %s", node.lastVoucher.Amount, quote.AmountToken)
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
}
