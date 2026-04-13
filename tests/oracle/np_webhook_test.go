package oracle_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	swapv1 "nhbchain/proto/swap/v1"
	oracle "nhbchain/services/oracle-attesterd"
)

type stubVerifier struct {
	err   error
	calls []verifyCall
}

type verifyCall struct {
	Hash   common.Hash
	Asset  oracle.Asset
	Amount *big.Int
}

func (s *stubVerifier) Confirm(_ context.Context, hash common.Hash, asset oracle.Asset, collector common.Address, amount *big.Int, confirmations uint64) error {
	cloned := new(big.Int).Set(amount)
	s.calls = append(s.calls, verifyCall{Hash: hash, Asset: asset, Amount: cloned})
	if s.err != nil {
		return s.err
	}
	if collector == (common.Address{}) {
		return fmt.Errorf("collector missing")
	}
	if confirmations != 0 {
		return fmt.Errorf("unexpected confirmation requirement: %d", confirmations)
	}
	return nil
}

type stubSubmitter struct {
	err   error
	calls []submitCall
}

type submitCall struct {
	Nonce uint64
	Msg   *swapv1.MsgMintDepositVoucher
}

func (s *stubSubmitter) Submit(_ context.Context, msg *swapv1.MsgMintDepositVoucher, nonce uint64) error {
	s.calls = append(s.calls, submitCall{Nonce: nonce, Msg: msg})
	return s.err
}

func TestNP_BadSig_Rejected(t *testing.T) {
	server, _, submitter := newTestServer(t)

	body := webhookPayload("np-1", "USDC", "100.00", "0xabc", time.Now())
	req := httptest.NewRequest(http.MethodPost, "/np/webhook", bytes.NewReader(body))
	req.Header.Set(headerNowPaymentsSignature, "deadbeef")

	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.Code)
	}
	if len(submitter.calls) != 0 {
		t.Fatalf("unexpected submitter calls: %d", len(submitter.calls))
	}
}

func TestNP_NoOnchainMatch_Rejected(t *testing.T) {
	server, verifier, submitter := newTestServer(t)
	verifier.err = fmt.Errorf("no match")

	body := webhookPayload("np-2", "USDC", "25.50", "0xdef", time.Now())
	req := httptest.NewRequest(http.MethodPost, "/np/webhook", bytes.NewReader(body))
	req.Header.Set(headerNowPaymentsSignature, signBody(testSecret, body))

	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.Code)
	}
	if len(verifier.calls) != 1 {
		t.Fatalf("expected verifier to be called once, got %d", len(verifier.calls))
	}
	if len(submitter.calls) != 0 {
		t.Fatalf("submitter called on rejection")
	}

	state, err := server.Store().Reserve("np-2")
	if err != nil {
		t.Fatalf("reserve after rejection: %v", err)
	}
	if state != oracle.InvoiceStateNew {
		t.Fatalf("expected invoice to be released, got state %v", state)
	}
	_ = server.Store().Release("np-2")
}

func TestNP_Valid_MintsOnce(t *testing.T) {
	server, verifier, submitter := newTestServer(t)

	body := webhookPayload("np-3", "USDC", "1.00", "0x123", time.Now())
	req := httptest.NewRequest(http.MethodPost, "/np/webhook", bytes.NewReader(body))
	req.Header.Set(headerNowPaymentsSignature, signBody(testSecret, body))

	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if len(verifier.calls) != 1 {
		t.Fatalf("verifier expected once, got %d", len(verifier.calls))
	}
	if len(submitter.calls) != 1 {
		t.Fatalf("submitter expected once, got %d", len(submitter.calls))
	}
	if submitter.calls[0].Nonce != 1 {
		t.Fatalf("expected nonce 1, got %d", submitter.calls[0].Nonce)
	}

	replay := httptest.NewRequest(http.MethodPost, "/np/webhook", bytes.NewReader(body))
	replay.Header.Set(headerNowPaymentsSignature, signBody(testSecret, body))
	replayResp := httptest.NewRecorder()
	server.ServeHTTP(replayResp, replay)

	if replayResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on replay, got %d", replayResp.Code)
	}
	if len(submitter.calls) != 1 {
		t.Fatalf("submitter invoked more than once on replay")
	}
}

const (
	testSecret                 = "secret"
	headerNowPaymentsSignature = "X-Nowpayments-Signature"
)

func newTestServer(t *testing.T) (*oracle.Server, *stubVerifier, *stubSubmitter) {
	t.Helper()
	dir := t.TempDir()
	store, err := oracle.NewInvoiceStore(filepath.Join(dir, "invoices.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	verifier := &stubVerifier{}
	submitter := &stubSubmitter{}

	cfg := oracle.Config{
		Provider:          "NOWPAYMENTS",
		Authority:         "authority",
		TreasuryAccount:   "nhb1test",
		CollectorAddress:  "0x00000000000000000000000000000000000000aa",
		Confirmations:     0,
		NonceStart:        1,
		NowPaymentsSecret: testSecret,
		RequestTimeout:    time.Second,
		Assets: []oracle.AssetConfig{{
			Symbol:   "USDC",
			Address:  "0x00000000000000000000000000000000000000bb",
			Decimals: 6,
		}},
	}

	server, err := oracle.NewServer(cfg, store, verifier, submitter)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return server, verifier, submitter
}

func webhookPayload(invoice, asset, amount, txHash string, created time.Time) []byte {
	payload := map[string]interface{}{
		"invoice_id":       invoice,
		"payment_status":   "confirmed",
		"pay_currency":     asset,
		"actually_paid":    amount,
		"transaction_hash": txHash,
		"created_at":       created.UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(payload)
	return body
}

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
