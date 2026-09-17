package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"nhbchain/crypto"
)

// p2p_createTrade/settle/dispute/resolve were disabled (see
// p2pRPCDisabledMessage in p2p_handlers.go) -- they used to mutate the live
// state trie directly outside the block pipeline, guaranteeing a consensus
// fork/halt on this 2-validator zero-quorum-slack chain the moment any of
// them was called. Every mutating handler now unconditionally returns
// codeMethodDisabled regardless of input, so the old per-field validation
// tests (invalid buyer, bad token, zero amount, past deadline, invalid
// outcome, forbidden caller, full create-then-settle round trips) no longer
// have anything left to exercise -- replaced with a direct check that each
// disabled method actually returns the disabled error. p2p_getPeers/getTrade
// (read-only, unaffected) keep their real behavioral tests below.

func TestP2PCreateTradeDisabled(t *testing.T) {
	env := newTestEnv(t)
	buyerKey, _ := crypto.GeneratePrivateKey()
	sellerKey, _ := crypto.GeneratePrivateKey()
	payload := map[string]interface{}{
		"offerId":     "OFF_1",
		"buyer":       buyerKey.PubKey().Address().String(),
		"seller":      sellerKey.PubKey().Address().String(),
		"baseToken":   "NHB",
		"baseAmount":  "1",
		"quoteToken":  "ZNHB",
		"quoteAmount": "1",
		"deadline":    int64(0),
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleP2PCreateTrade(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestP2PSettleDisabled(t *testing.T) {
	env := newTestEnv(t)
	payload := map[string]string{
		"tradeId": "0x" + strings.Repeat("0", 64),
		"caller":  "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
	}
	req := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleP2PSettle(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestP2PDisputeDisabled(t *testing.T) {
	env := newTestEnv(t)
	payload := map[string]string{
		"tradeId": "0x" + strings.Repeat("0", 64),
		"caller":  "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
		"message": "dispute",
	}
	req := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleP2PDispute(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestP2PResolveDisabled(t *testing.T) {
	env := newTestEnv(t)
	payload := map[string]string{
		"tradeId": "0x" + strings.Repeat("0", 64),
		"caller":  "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
		"outcome": "buyer",
	}
	req := &RPCRequest{ID: 4, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleP2PResolve(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestP2PGetTradeNotFound(t *testing.T) {
	env := newTestEnv(t)
	payload := map[string]string{"tradeId": "0x" + strings.Repeat("0", 64)}
	req := &RPCRequest{ID: 10, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleP2PGetTrade(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeP2PNotFound {
		t.Fatalf("expected code %d got %d", codeP2PNotFound, rpcErr.Code)
	}
}
