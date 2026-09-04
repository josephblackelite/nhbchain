package rpc

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhbchain/crypto"
	escrow "nhbchain/native/escrow"
)

// escrow_create/fund/release/refund/dispute/expire/resolve were disabled
// (see escrowRPCDisabledMessage in escrow_handlers.go) -- they used to
// mutate the live state trie directly outside the block pipeline,
// guaranteeing a consensus fork/halt on this 2-validator zero-quorum-slack
// chain the moment any of them was called. Every mutating handler now
// unconditionally returns codeMethodDisabled regardless of input, so the
// old per-field validation tests (invalid bech32, bad token, zero amount,
// fee too high, forbidden caller, full create-then-get round trips) no
// longer have anything left to exercise -- replaced with a direct check
// that each disabled method actually returns the disabled error.
// escrow_get (read-only, unaffected) keeps its real behavioral test below.

func TestEscrowCreateDisabled(t *testing.T) {
	env := newTestEnv(t)
	payerKey, _ := crypto.GeneratePrivateKey()
	payeeKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(time.Minute).Unix()
	payload := map[string]interface{}{
		"payer":    payerKey.PubKey().Address().String(),
		"payee":    payeeKey.PubKey().Address().String(),
		"token":    "NHB",
		"amount":   "1",
		"feeBps":   0,
		"deadline": deadline,
		"nonce":    1,
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleEscrowCreate(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestEscrowFundDisabled(t *testing.T) {
	env := newTestEnv(t)
	fromKey, _ := crypto.GeneratePrivateKey()
	payload := map[string]string{
		"id":   "0x" + strings.Repeat("00", 32),
		"from": fromKey.PubKey().Address().String(),
	}
	req := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleEscrowFund(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestEscrowGetNotFound(t *testing.T) {
	env := newTestEnv(t)
	payload := map[string]string{"id": "0x" + strings.Repeat("00", 32)}
	req := &RPCRequest{ID: 5, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleEscrowGet(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeEscrowNotFound {
		t.Fatalf("expected code %d got %d", codeEscrowNotFound, rpcErr.Code)
	}
	if rpcErr.Message != "not_found" {
		t.Fatalf("expected message not_found got %s", rpcErr.Message)
	}
}

func TestFormatEscrowJSONIncludesReason(t *testing.T) {
	var id [32]byte
	copy(id[:], bytes.Repeat([]byte{0xAB}, 32))
	var payer [20]byte
	copy(payer[:], bytes.Repeat([]byte{0x01}, 20))
	var payee [20]byte
	copy(payee[:], bytes.Repeat([]byte{0x02}, 20))
	esc := &escrow.Escrow{
		ID:            id,
		Payer:         payer,
		Payee:         payee,
		Token:         "NHB",
		Amount:        big.NewInt(10),
		FeeBps:        50,
		Deadline:      1234,
		CreatedAt:     1200,
		Nonce:         1,
		Status:        escrow.EscrowFunded,
		MetaHash:      [32]byte{0xAA},
		DisputeReason: "item damaged",
	}
	formatted := formatEscrowJSON(esc)
	if formatted.DisputeReason == nil {
		t.Fatalf("expected dispute reason pointer")
	}
	if *formatted.DisputeReason != "item damaged" {
		t.Fatalf("unexpected dispute reason: %q", *formatted.DisputeReason)
	}
}
