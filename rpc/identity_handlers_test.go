package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nhbchain/crypto"
)

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
	if !ok || resolved != decodedAddr {
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
	if resolveResp.Address != addr {
		t.Fatalf("expected address %s, got %s", addr, resolveResp.Address)
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
