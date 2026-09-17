package rpc

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// newFeesQuoteTestServer builds a node whose TransferGasPolicy free tier is
// forced to zero, so fees_getTransferQuote always reflects the live,
// protocol-enforced per-asset rate (never a waived/zero fee), and returns the
// server plus a bech32 wallet address to quote against.
func newFeesQuoteTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	policy := node.TransferGasPolicy()
	policy.FreeSpendLimitWei = big.NewInt(0)
	node.SetTransferGasPolicy(policy)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	senderAddrStr := senderKey.PubKey().Address().String()

	server := newTestServer(t, node, nil, ServerConfig{})
	return server, senderAddrStr
}

func decodeFeesTransferQuoteResult(t *testing.T, recorder *httptest.ResponseRecorder) feesTransferQuoteResult {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var resp RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	var result feesTransferQuoteResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return result
}

// TestHandleFeesGetTransferQuote_NHBUsesNHBRate proves fees_getTransferQuote
// echoes back NHB's own 20bps rate (feeBps and the correspondingly sized
// feeWei), per docs/issue30.md item 7b / the NHB-vs-ZNHB fee split.
func TestHandleFeesGetTransferQuote_NHBUsesNHBRate(t *testing.T) {
	server, senderAddrStr := newFeesQuoteTestServer(t)

	param, err := json.Marshal(map[string]interface{}{
		"address":   senderAddrStr,
		"asset":     "NHB",
		"amountWei": "250000",
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{param}}
	recorder := httptest.NewRecorder()
	server.handleFeesGetTransferQuote(recorder, httptest.NewRequest(http.MethodPost, "/", nil), req)

	result := decodeFeesTransferQuoteResult(t, recorder)
	if result.Eligible {
		t.Fatalf("expected ineligible (free tier forced to zero), got eligible")
	}
	if result.FeeBps != 20 {
		t.Fatalf("expected feeBps 20 for an NHB quote, got %d", result.FeeBps)
	}
	// 20 bps of 250000 = 500.
	if result.FeeWei != "500" {
		t.Fatalf("expected feeWei 500 (20bps of 250000), got %s", result.FeeWei)
	}
}

// TestHandleFeesGetTransferQuote_ZNHBUsesZNHBRate proves fees_getTransferQuote
// echoes back ZNHB's own, separately configured 10bps rate -- not NHB's
// 20bps -- so nhbportal's wallet UI (which reads this per-request feeBps
// value directly) displays the correct, lower ZNHB fee with no portal-side
// change required.
func TestHandleFeesGetTransferQuote_ZNHBUsesZNHBRate(t *testing.T) {
	server, senderAddrStr := newFeesQuoteTestServer(t)

	param, err := json.Marshal(map[string]interface{}{
		"address":   senderAddrStr,
		"asset":     "ZNHB",
		"amountWei": "250000",
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{param}}
	recorder := httptest.NewRecorder()
	server.handleFeesGetTransferQuote(recorder, httptest.NewRequest(http.MethodPost, "/", nil), req)

	result := decodeFeesTransferQuoteResult(t, recorder)
	if result.Eligible {
		t.Fatalf("expected ineligible (free tier forced to zero), got eligible")
	}
	if result.FeeBps != 10 {
		t.Fatalf("expected feeBps 10 for a ZNHB quote, got %d", result.FeeBps)
	}
	// 10 bps of 250000 = 250 -- half the NHB quote's 500, and must come
	// from ZNHB's own rate, not a leftover use of NHB's.
	if result.FeeWei != "250" {
		t.Fatalf("expected feeWei 250 (10bps of 250000), got %s", result.FeeWei)
	}
}
