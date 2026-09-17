package rpc

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

func fundAccountNHB(t *testing.T, node *core.Node, addr [20]byte, amount int64) {
	t.Helper()
	if err := node.WithState(func(m *nhbstate.Manager) error {
		account, err := m.GetAccount(addr[:])
		if err != nil {
			return err
		}
		if account == nil {
			account = &types.Account{}
		}
		if account.BalanceNHB == nil {
			account.BalanceNHB = big.NewInt(0)
		}
		account.BalanceNHB = big.NewInt(amount)
		return m.PutAccount(addr[:], account)
	}); err != nil {
		t.Fatalf("fund account: %v", err)
	}
}

// identity_setAlias/setAvatar/addAddress/removeAddress/setPrimary/rename/
// createClaimable/claim were disabled (see identityRPCDisabledMessage in
// identity_handlers.go) -- they used to mutate the live state trie directly
// outside the block pipeline, guaranteeing a consensus fork/halt on this
// 2-validator zero-quorum-slack chain the moment any of them was called.
// Every mutating handler now unconditionally returns codeMethodDisabled
// regardless of input, so the old success-path tests (which all depended on
// setAlias/createClaimable actually writing state) no longer have anything
// left to exercise -- replaced with a direct check that each disabled
// method actually returns the disabled error. identity_resolve/reverse
// (read-only, unaffected) keep their real behavioral tests below.

func TestIdentitySetAliasDisabled(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().String()
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, addr), marshalParam(t, "frankrocks")}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentitySetAlias(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestIdentitySetAvatarDisabled(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().String()
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, addr), marshalParam(t, "https://cdn.example/avatar.png")}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentitySetAvatar(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestIdentityAddAddressDisabled(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	owner := key.PubKey().Address().String()
	payload := map[string]string{"owner": owner, "alias": "builder", "address": owner}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentityAddAddress(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestIdentityRemoveAddressDisabled(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	owner := key.PubKey().Address().String()
	payload := map[string]string{"owner": owner, "alias": "builder", "address": owner}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentityRemoveAddress(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestIdentitySetPrimaryDisabled(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	owner := key.PubKey().Address().String()
	payload := map[string]string{"owner": owner, "alias": "builder", "address": owner}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentitySetPrimary(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestIdentityRenameDisabled(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	owner := key.PubKey().Address().String()
	payload := map[string]string{"owner": owner, "alias": "builder", "newAlias": "artisan"}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentityRename(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestIdentityCreateClaimableDisabled(t *testing.T) {
	env := newTestEnv(t)
	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer: %v", err)
	}
	payload := map[string]interface{}{
		"payer":     payerKey.PubKey().Address().String(),
		"recipient": "frankrocks",
		"token":     "NHB",
		"amount":    "100",
		"deadline":  int64(0),
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentityCreateClaimable(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestIdentityClaimDisabled(t *testing.T) {
	env := newTestEnv(t)
	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee: %v", err)
	}
	payload := map[string]interface{}{
		"claimId":  "0x" + strings.Repeat("00", 32),
		"payee":    payeeKey.PubKey().Address().String(),
		"preimage": "0x" + strings.Repeat("00", 32),
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentityClaim(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}

func TestHandleIdentityResolveNotFound(t *testing.T) {
	env := newTestEnv(t)
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, "unknown")}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentityResolve(recorder, nil, req)
	if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusNotFound {
		t.Fatalf("expected error status, got %d", recorder.Code)
	}
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected rpc error")
	}
}

func TestHandleIdentityReverseNotFound(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().String()
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, addr)}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentityReverse(recorder, nil, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404 status, got %d", recorder.Code)
	}
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil || rpcErr.Message != "address has no alias" {
		t.Fatalf("expected address has no alias error, got %+v", rpcErr)
	}
}
