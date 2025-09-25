package rpc

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/native/loyalty"
	swap "nhbchain/native/swap"
	"nhbchain/storage"
)

type testEnv struct {
	server *Server
	node   *core.Node
	token  string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	token := "test-token"
	if err := os.Setenv("NHB_RPC_TOKEN", token); err != nil {
		t.Fatalf("set env: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("NHB_RPC_TOKEN")
	})
	db := storage.NewMemDB()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	node, err := core.NewNode(db, key, "", true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	node.SetSwapConfig(swap.Config{AllowedFiat: []string{"USD"}, MaxQuoteAgeSeconds: 120, SlippageBps: 50, OraclePriority: []string{"manual"}})
	manual := swap.NewManualOracle()
	agg := swap.NewOracleAggregator([]string{"manual"}, 5*time.Minute)
	agg.Register("manual", manual)
	node.SetSwapOracle(agg)
	node.SetSwapManualOracle(manual)
	server := NewServer(node)
	return &testEnv{server: server, node: node, token: token}
}

func (env *testEnv) newRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+env.token)
	return req
}

func marshalParam(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal param: %v", err)
	}
	return raw
}

func decodeRPCResponse(t *testing.T, rec *httptest.ResponseRecorder) (json.RawMessage, *RPCError) {
	t.Helper()
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.Result, resp.Error
}

func decodeBusinessID(t *testing.T, idStr string) loyalty.BusinessID {
	t.Helper()
	id, err := parseBusinessID(idStr)
	if err != nil {
		t.Fatalf("parse business id: %v", err)
	}
	return id
}

func decodeProgramID(t *testing.T, idStr string) loyalty.ProgramID {
	id, err := parseProgramID(idStr)
	if err != nil {
		t.Fatalf("parse program id: %v", err)
	}
	return id
}

func TestHandleLoyaltyCreateBusinessSuccess(t *testing.T) {
	env := newTestEnv(t)
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner: %v", err)
	}
	ownerAddr := ownerKey.PubKey().Address().String()

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller": ownerAddr,
		"name":   "Acme Corp",
	})}}
	recorder := httptest.NewRecorder()
	env.server.handleLoyaltyCreateBusiness(recorder, env.newRequest(), req)

	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var businessID string
	if err := json.Unmarshal(result, &businessID); err != nil {
		t.Fatalf("decode business id: %v", err)
	}
	id := decodeBusinessID(t, businessID)
	business, ok, err := env.node.LoyaltyBusinessByID(id)
	if err != nil {
		t.Fatalf("load business: %v", err)
	}
	if !ok {
		t.Fatalf("business not found")
	}
	if business.Name != "Acme Corp" {
		t.Fatalf("unexpected business name: %s", business.Name)
	}
	if crypto.NewAddress(crypto.NHBPrefix, business.Owner[:]).String() != ownerAddr {
		t.Fatalf("owner mismatch")
	}
}

func TestHandleLoyaltyCreateBusinessInvalidAddress(t *testing.T) {
	env := newTestEnv(t)
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller": "invalid",
		"name":   "Bad",
	})}}
	recorder := httptest.NewRecorder()
	env.server.handleLoyaltyCreateBusiness(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params code, got %d", rpcErr.Code)
	}
}

func TestHandleLoyaltySetPaymasterUnauthorized(t *testing.T) {
	env := newTestEnv(t)
	ownerKey, _ := crypto.GeneratePrivateKey()
	ownerAddr := ownerKey.PubKey().Address().String()
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller": ownerAddr,
		"name":   "Biz",
	})}}
	recorder := httptest.NewRecorder()
	env.server.handleLoyaltyCreateBusiness(recorder, env.newRequest(), req)
	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected error creating business: %+v", rpcErr)
	}
	var businessID string
	if err := json.Unmarshal(result, &businessID); err != nil {
		t.Fatalf("decode business id: %v", err)
	}

	outsiderKey, _ := crypto.GeneratePrivateKey()
	outsiderAddr := outsiderKey.PubKey().Address().String()
	payload := map[string]string{
		"caller":     outsiderAddr,
		"businessId": businessID,
		"paymaster":  ownerAddr,
	}
	setReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, payload)}}
	setRecorder := httptest.NewRecorder()
	env.server.handleLoyaltySetPaymaster(setRecorder, env.newRequest(), setReq)
	_, setErr := decodeRPCResponse(t, setRecorder)
	if setErr == nil {
		t.Fatalf("expected unauthorized error")
	}
	if setErr.Code != codeUnauthorized {
		t.Fatalf("expected code %d got %d", codeUnauthorized, setErr.Code)
	}
}

func TestHandleLoyaltyCreateProgramSuccess(t *testing.T) {
	env := newTestEnv(t)
	manager := env.node.LoyaltyManager()
	if err := manager.RegisterToken("ZNHB", "Zap", 18); err != nil {
		t.Fatalf("register token: %v", err)
	}
	ownerKey, _ := crypto.GeneratePrivateKey()
	ownerAddr := ownerKey.PubKey().Address().String()
	businessReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller": ownerAddr,
		"name":   "Rewards",
	})}}
	bizRec := httptest.NewRecorder()
	env.server.handleLoyaltyCreateBusiness(bizRec, env.newRequest(), businessReq)
	bizResult, bizErr := decodeRPCResponse(t, bizRec)
	if bizErr != nil {
		t.Fatalf("create business: %+v", bizErr)
	}
	var businessID string
	if err := json.Unmarshal(bizResult, &businessID); err != nil {
		t.Fatalf("decode business id: %v", err)
	}

	merchantKey, _ := crypto.GeneratePrivateKey()
	merchantAddr := merchantKey.PubKey().Address().String()
	addReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller":     ownerAddr,
		"businessId": businessID,
		"merchant":   merchantAddr,
	})}}
	addRec := httptest.NewRecorder()
	env.server.handleLoyaltyAddMerchant(addRec, env.newRequest(), addReq)
	_, addErr := decodeRPCResponse(t, addRec)
	if addErr != nil {
		t.Fatalf("add merchant: %+v", addErr)
	}

	var programID [32]byte
	programID[31] = 1
	programIDHex := "0x" + hex.EncodeToString(programID[:])
	poolKey, _ := crypto.GeneratePrivateKey()
	poolAddr := poolKey.PubKey().Address().String()
	spec := map[string]interface{}{
		"id":          programIDHex,
		"owner":       merchantAddr,
		"pool":        poolAddr,
		"tokenSymbol": "ZNHB",
		"accrualBps":  100,
	}
	envReq := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, map[string]interface{}{
		"caller":     merchantAddr,
		"businessId": businessID,
		"spec":       spec,
	})}}
	envRec := httptest.NewRecorder()
	env.server.handleLoyaltyCreateProgram(envRec, env.newRequest(), envReq)
	programResult, programErr := decodeRPCResponse(t, envRec)
	if programErr != nil {
		t.Fatalf("create program error: %+v", programErr)
	}
	var returnedID string
	if err := json.Unmarshal(programResult, &returnedID); err != nil {
		t.Fatalf("decode program id: %v", err)
	}
	if returnedID != programIDHex {
		t.Fatalf("unexpected program id: %s", returnedID)
	}
	loaded, ok, err := env.node.LoyaltyProgramByID(decodeProgramID(t, returnedID))
	if err != nil {
		t.Fatalf("load program: %v", err)
	}
	if !ok {
		t.Fatalf("program not found")
	}
	if !loaded.Active || loaded.AccrualBps != 100 {
		t.Fatalf("unexpected program state")
	}
}
