package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestHandleIdentitySetAliasSuccess(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().String()

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, addr), marshalParam(t, "frankrocks")}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentitySetAlias(recorder, env.newRequest(), req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var response identitySetAliasResult
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !response.OK {
		t.Fatalf("expected ok true")
	}
	decodedAddr, err := decodeBech32(addr)
	if err != nil {
		t.Fatalf("decode address: %v", err)
	}
	resolved, ok := env.node.IdentityResolve("frankrocks")
	if !ok || resolved == nil || resolved.Primary != decodedAddr {
		t.Fatalf("alias not stored")
	}
}

func TestHandleIdentitySetAliasDuplicate(t *testing.T) {
	env := newTestEnv(t)
	first, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate first key: %v", err)
	}
	second, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate second key: %v", err)
	}
	firstAddr := first.PubKey().Address().String()
	secondAddr := second.PubKey().Address().String()

	// Seed initial alias
	seedReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, firstAddr), marshalParam(t, "shared")}}
	env.server.handleIdentitySetAlias(httptest.NewRecorder(), env.newRequest(), seedReq)

	req := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, secondAddr), marshalParam(t, "shared")}}
	recorder := httptest.NewRecorder()
	env.server.handleIdentitySetAlias(recorder, env.newRequest(), req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 status, got %d", recorder.Code)
	}
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil || rpcErr.Message != "alias already registered" {
		t.Fatalf("expected alias already registered error, got %+v", rpcErr)
	}
}

func TestHandleIdentitySetAvatar(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().String()
	setAlias := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, addr), marshalParam(t, "avataruser")}}
	env.server.handleIdentitySetAlias(httptest.NewRecorder(), env.newRequest(), setAlias)

	avatarReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, addr), marshalParam(t, "https://cdn.example/avatar.png")}}
	avatarRec := httptest.NewRecorder()
	env.server.handleIdentitySetAvatar(avatarRec, env.newRequest(), avatarReq)
	if avatarRec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", avatarRec.Code)
	}
	result, rpcErr := decodeRPCResponse(t, avatarRec)
	if rpcErr != nil {
		t.Fatalf("unexpected avatar error: %+v", rpcErr)
	}
	var resp struct {
		OK        bool   `json:"ok"`
		Alias     string `json:"alias"`
		AliasID   string `json:"aliasId"`
		AvatarRef string `json:"avatarRef"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("decode avatar result: %v", err)
	}
	if !resp.OK || resp.Alias != "avataruser" || resp.AvatarRef == "" {
		t.Fatalf("unexpected avatar response: %+v", resp)
	}
	resolved, ok := env.node.IdentityResolve("avataruser")
	if !ok || resolved == nil || resolved.AvatarRef != resp.AvatarRef {
		t.Fatalf("avatar not stored")
	}
}

func TestHandleIdentityResolveAndReverse(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().String()

	// set alias first
	setReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, addr), marshalParam(t, "resolver")}}
	env.server.handleIdentitySetAlias(httptest.NewRecorder(), env.newRequest(), setReq)

	resolveReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, "resolver")}}
	resolveRec := httptest.NewRecorder()
	env.server.handleIdentityResolve(resolveRec, nil, resolveReq)
	if resolveRec.Code != http.StatusOK {
		t.Fatalf("unexpected resolve status: %d", resolveRec.Code)
	}
	result, rpcErr := decodeRPCResponse(t, resolveRec)
	if rpcErr != nil {
		t.Fatalf("unexpected resolve error: %+v", rpcErr)
	}
	var resolveResp identityResolveResult
	if err := json.Unmarshal(result, &resolveResp); err != nil {
		t.Fatalf("decode resolve result: %v", err)
	}
	if resolveResp.Primary != addr {
		t.Fatalf("expected primary address %s, got %s", addr, resolveResp.Primary)
	}
	if resolveResp.Alias != "resolver" {
		t.Fatalf("expected alias resolver, got %s", resolveResp.Alias)
	}
	if resolveResp.AliasID == "" {
		t.Fatalf("expected aliasId in response")
	}
	if len(resolveResp.Addresses) == 0 || resolveResp.Addresses[0] != addr {
		t.Fatalf("expected addresses list to include primary")
	}

	reverseReq := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, addr)}}
	reverseRec := httptest.NewRecorder()
	env.server.handleIdentityReverse(reverseRec, nil, reverseReq)
	if reverseRec.Code != http.StatusOK {
		t.Fatalf("unexpected reverse status: %d", reverseRec.Code)
	}
	revResult, rpcErr := decodeRPCResponse(t, reverseRec)
	if rpcErr != nil {
		t.Fatalf("unexpected reverse error: %+v", rpcErr)
	}
	var reverseResp identityReverseResult
	if err := json.Unmarshal(revResult, &reverseResp); err != nil {
		t.Fatalf("decode reverse result: %v", err)
	}
	if reverseResp.Alias != "resolver" {
		t.Fatalf("expected alias resolver, got %s", reverseResp.Alias)
	}
	if reverseResp.AliasID == "" {
		t.Fatalf("expected aliasId in reverse response")
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

func TestHandleIdentityClaimableFlow(t *testing.T) {
	env := newTestEnv(t)
	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer: %v", err)
	}
	payerAddr := payerKey.PubKey().Address()
	var payer [20]byte
	copy(payer[:], payerAddr.Bytes())
	fundAccountNHB(t, env.node, payer, 1_000)

	deadline := time.Now().Add(time.Hour).Unix()
	var hint [32]byte
	hint[31] = 0xAA
	hintHex := "0x" + hex.EncodeToString(hint[:])

	createPayload := map[string]interface{}{
		"payer":     payerAddr.String(),
		"recipient": hintHex,
		"token":     "NHB",
		"amount":    "100",
		"deadline":  deadline,
	}
	createReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, createPayload)}}
	createRec := httptest.NewRecorder()
	env.server.handleIdentityCreateClaimable(createRec, env.newRequest(), createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("unexpected create status: %d", createRec.Code)
	}
	createResult, createErr := decodeRPCResponse(t, createRec)
	if createErr != nil {
		t.Fatalf("create claimable error: %+v", createErr)
	}
	var createResp identityCreateClaimableResult
	if err := json.Unmarshal(createResult, &createResp); err != nil {
		t.Fatalf("decode create result: %v", err)
	}
	if createResp.ClaimID == "" || createResp.RecipientHint != hintHex {
		t.Fatalf("unexpected create response: %+v", createResp)
	}

	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee: %v", err)
	}
	payeeAddr := payeeKey.PubKey().Address().String()

	claimPayload := map[string]interface{}{
		"claimId":  createResp.ClaimID,
		"payee":    payeeAddr,
		"preimage": hintHex,
	}
	claimReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, claimPayload)}}
	claimRec := httptest.NewRecorder()
	env.server.handleIdentityClaim(claimRec, env.newRequest(), claimReq)
	if claimRec.Code != http.StatusOK {
		t.Fatalf("unexpected claim status: %d", claimRec.Code)
	}
	claimResult, claimErr := decodeRPCResponse(t, claimRec)
	if claimErr != nil {
		t.Fatalf("claim error: %+v", claimErr)
	}
	var claimResp identityClaimResult
	if err := json.Unmarshal(claimResult, &claimResp); err != nil {
		t.Fatalf("decode claim result: %v", err)
	}
	if !claimResp.OK || claimResp.Amount != "100" || claimResp.Token != "NHB" {
		t.Fatalf("unexpected claim response: %+v", claimResp)
	}
	var payee [20]byte
	copy(payee[:], payeeKey.PubKey().Address().Bytes())
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		account, err := m.GetAccount(payee[:])
		if err != nil {
			return err
		}
		if account == nil || account.BalanceNHB.Cmp(big.NewInt(100)) != 0 {
			return fmt.Errorf("unexpected payee balance")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify payee balance: %v", err)
	}
}
