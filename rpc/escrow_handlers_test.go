package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhbchain/crypto"
)

func TestEscrowCreateInvalidBech32(t *testing.T) {
	env := newTestEnv(t)
	payeeKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(time.Minute).Unix()
	payload := map[string]interface{}{
		"payer":    "invalid",
		"payee":    payeeKey.PubKey().Address().String(),
		"token":    "NHB",
		"amount":   "1",
		"feeBps":   0,
		"deadline": deadline,
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleEscrowCreate(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeEscrowInvalidParams {
		t.Fatalf("expected code %d got %d", codeEscrowInvalidParams, rpcErr.Code)
	}
	if rpcErr.Message != "invalid_params" {
		t.Fatalf("expected message invalid_params got %s", rpcErr.Message)
	}
}

func TestEscrowCreateBadToken(t *testing.T) {
	env := newTestEnv(t)
	payerKey, _ := crypto.GeneratePrivateKey()
	payeeKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(time.Minute).Unix()
	payload := map[string]interface{}{
		"payer":    payerKey.PubKey().Address().String(),
		"payee":    payeeKey.PubKey().Address().String(),
		"token":    "DOGE",
		"amount":   "1",
		"feeBps":   0,
		"deadline": deadline,
	}
	req := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleEscrowCreate(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeEscrowInvalidParams {
		t.Fatalf("expected code %d got %d", codeEscrowInvalidParams, rpcErr.Code)
	}
}

func TestEscrowCreateZeroAmount(t *testing.T) {
	env := newTestEnv(t)
	payerKey, _ := crypto.GeneratePrivateKey()
	payeeKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(time.Minute).Unix()
	payload := map[string]interface{}{
		"payer":    payerKey.PubKey().Address().String(),
		"payee":    payeeKey.PubKey().Address().String(),
		"token":    "NHB",
		"amount":   "0",
		"feeBps":   0,
		"deadline": deadline,
	}
	req := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleEscrowCreate(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeEscrowInvalidParams {
		t.Fatalf("expected code %d got %d", codeEscrowInvalidParams, rpcErr.Code)
	}
}

func TestEscrowCreateFeeTooHigh(t *testing.T) {
	env := newTestEnv(t)
	payerKey, _ := crypto.GeneratePrivateKey()
	payeeKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(time.Minute).Unix()
	payload := map[string]interface{}{
		"payer":    payerKey.PubKey().Address().String(),
		"payee":    payeeKey.PubKey().Address().String(),
		"token":    "NHB",
		"amount":   "10",
		"feeBps":   10001,
		"deadline": deadline,
	}
	req := &RPCRequest{ID: 4, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleEscrowCreate(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeEscrowInvalidParams {
		t.Fatalf("expected code %d got %d", codeEscrowInvalidParams, rpcErr.Code)
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

func TestEscrowFundWrongCaller(t *testing.T) {
	env := newTestEnv(t)
	payerKey, _ := crypto.GeneratePrivateKey()
	payeeKey, _ := crypto.GeneratePrivateKey()
	outsiderKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(time.Minute).Unix()
	createPayload := map[string]interface{}{
		"payer":    payerKey.PubKey().Address().String(),
		"payee":    payeeKey.PubKey().Address().String(),
		"token":    "NHB",
		"amount":   "5",
		"feeBps":   0,
		"deadline": deadline,
	}
	createReq := &RPCRequest{ID: 6, Params: []json.RawMessage{marshalParam(t, createPayload)}}
	createRec := httptest.NewRecorder()
	env.server.handleEscrowCreate(createRec, env.newRequest(), createReq)
	result, createErr := decodeRPCResponse(t, createRec)
	if createErr != nil {
		t.Fatalf("unexpected create error: %+v", createErr)
	}
	var createRes escrowCreateResult
	if err := json.Unmarshal(result, &createRes); err != nil {
		t.Fatalf("decode create result: %v", err)
	}
	fundPayload := map[string]string{
		"id":   createRes.ID,
		"from": outsiderKey.PubKey().Address().String(),
	}
	fundReq := &RPCRequest{ID: 7, Params: []json.RawMessage{marshalParam(t, fundPayload)}}
	fundRec := httptest.NewRecorder()
	env.server.handleEscrowFund(fundRec, env.newRequest(), fundReq)
	_, fundErr := decodeRPCResponse(t, fundRec)
	if fundErr == nil {
		t.Fatalf("expected forbidden error")
	}
	if fundErr.Code != codeEscrowForbidden {
		t.Fatalf("expected code %d got %d", codeEscrowForbidden, fundErr.Code)
	}
	if fundErr.Message != "forbidden" {
		t.Fatalf("expected message forbidden got %s", fundErr.Message)
	}
}

func TestEscrowCreateAndGet(t *testing.T) {
	env := newTestEnv(t)
	payerKey, _ := crypto.GeneratePrivateKey()
	payeeKey, _ := crypto.GeneratePrivateKey()
	mediatorKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(2 * time.Minute).Unix()
	before := time.Now().Unix()
	createPayload := map[string]interface{}{
		"payer":    payerKey.PubKey().Address().String(),
		"payee":    payeeKey.PubKey().Address().String(),
		"token":    "NHB",
		"amount":   "123",
		"feeBps":   250,
		"deadline": deadline,
		"mediator": mediatorKey.PubKey().Address().String(),
		"meta":     "0x1234",
	}
	createReq := &RPCRequest{ID: 8, Params: []json.RawMessage{marshalParam(t, createPayload)}}
	createRec := httptest.NewRecorder()
	env.server.handleEscrowCreate(createRec, env.newRequest(), createReq)
	createResult, createErr := decodeRPCResponse(t, createRec)
	if createErr != nil {
		t.Fatalf("create error: %+v", createErr)
	}
	var createRes escrowCreateResult
	if err := json.Unmarshal(createResult, &createRes); err != nil {
		t.Fatalf("decode create result: %v", err)
	}
	getPayload := map[string]string{"id": createRes.ID}
	getReq := &RPCRequest{ID: 9, Params: []json.RawMessage{marshalParam(t, getPayload)}}
	getRec := httptest.NewRecorder()
	env.server.handleEscrowGet(getRec, env.newRequest(), getReq)
	getResult, getErr := decodeRPCResponse(t, getRec)
	if getErr != nil {
		t.Fatalf("get error: %+v", getErr)
	}
	var esc escrowJSON
	if err := json.Unmarshal(getResult, &esc); err != nil {
		t.Fatalf("decode escrow json: %v", err)
	}
	if esc.ID != createRes.ID {
		t.Fatalf("unexpected id: %s", esc.ID)
	}
	if esc.Payer != createPayload["payer"].(string) {
		t.Fatalf("payer mismatch got %s", esc.Payer)
	}
	if esc.Payee != createPayload["payee"].(string) {
		t.Fatalf("payee mismatch got %s", esc.Payee)
	}
	if esc.Token != "NHB" {
		t.Fatalf("expected token NHB got %s", esc.Token)
	}
	if esc.Amount != "123" {
		t.Fatalf("expected amount 123 got %s", esc.Amount)
	}
	if esc.FeeBps != 250 {
		t.Fatalf("expected fee 250 got %d", esc.FeeBps)
	}
	if esc.Deadline != deadline {
		t.Fatalf("expected deadline %d got %d", deadline, esc.Deadline)
	}
	if esc.Status != "init" {
		t.Fatalf("expected status init got %s", esc.Status)
	}
	if esc.Meta == "" || !strings.HasPrefix(esc.Meta, "0x1234") {
		t.Fatalf("unexpected meta %s", esc.Meta)
	}
	if len(esc.Meta) != 66 {
		t.Fatalf("expected meta length 66 got %d", len(esc.Meta))
	}
	if esc.Mediator == nil || *esc.Mediator != createPayload["mediator"].(string) {
		t.Fatalf("mediator mismatch")
	}
	if esc.CreatedAt < before {
		t.Fatalf("createdAt too old: %d", esc.CreatedAt)
	}
	if esc.CreatedAt > time.Now().Unix() {
		t.Fatalf("createdAt in future: %d", esc.CreatedAt)
	}
}
