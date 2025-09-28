package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhbchain/crypto"
)

func TestP2PCreateTradeInvalidBuyer(t *testing.T) {
	env := newTestEnv(t)
	sellerKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(time.Minute).Unix()
	payload := map[string]interface{}{
		"offerId":     "OFF_1",
		"buyer":       "invalid",
		"seller":      sellerKey.PubKey().Address().String(),
		"baseToken":   "NHB",
		"baseAmount":  "1",
		"quoteToken":  "ZNHB",
		"quoteAmount": "1",
		"deadline":    deadline,
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleP2PCreateTrade(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeP2PInvalidParams {
		t.Fatalf("expected code %d got %d", codeP2PInvalidParams, rpcErr.Code)
	}
}

func TestP2PCreateTradeBadToken(t *testing.T) {
	env := newTestEnv(t)
	buyerKey, _ := crypto.GeneratePrivateKey()
	sellerKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(time.Minute).Unix()
	payload := map[string]interface{}{
		"offerId":     "OFF_2",
		"buyer":       buyerKey.PubKey().Address().String(),
		"seller":      sellerKey.PubKey().Address().String(),
		"baseToken":   "DOGE",
		"baseAmount":  "1",
		"quoteToken":  "ZNHB",
		"quoteAmount": "1",
		"deadline":    deadline,
	}
	req := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleP2PCreateTrade(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeP2PInvalidParams {
		t.Fatalf("expected code %d got %d", codeP2PInvalidParams, rpcErr.Code)
	}
}

func TestP2PCreateTradeZeroAmount(t *testing.T) {
	env := newTestEnv(t)
	buyerKey, _ := crypto.GeneratePrivateKey()
	sellerKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(time.Minute).Unix()
	payload := map[string]interface{}{
		"offerId":     "OFF_3",
		"buyer":       buyerKey.PubKey().Address().String(),
		"seller":      sellerKey.PubKey().Address().String(),
		"baseToken":   "NHB",
		"baseAmount":  "0",
		"quoteToken":  "ZNHB",
		"quoteAmount": "1",
		"deadline":    deadline,
	}
	req := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleP2PCreateTrade(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeP2PInvalidParams {
		t.Fatalf("expected code %d got %d", codeP2PInvalidParams, rpcErr.Code)
	}
}

func TestP2PCreateTradeDeadlinePast(t *testing.T) {
	env := newTestEnv(t)
	buyerKey, _ := crypto.GeneratePrivateKey()
	sellerKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(-time.Minute).Unix()
	payload := map[string]interface{}{
		"offerId":     "OFF_4",
		"buyer":       buyerKey.PubKey().Address().String(),
		"seller":      sellerKey.PubKey().Address().String(),
		"baseToken":   "NHB",
		"baseAmount":  "1",
		"quoteToken":  "ZNHB",
		"quoteAmount": "1",
		"deadline":    deadline,
	}
	req := &RPCRequest{ID: 4, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleP2PCreateTrade(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeP2PInvalidParams {
		t.Fatalf("expected code %d got %d", codeP2PInvalidParams, rpcErr.Code)
	}
}

func TestP2PResolveInvalidOutcome(t *testing.T) {
	env := newTestEnv(t)
	payload := map[string]string{
		"tradeId": "0x" + strings.Repeat("0", 64),
		"caller":  "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
		"outcome": "invalid",
	}
	req := &RPCRequest{ID: 5, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleP2PResolve(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeP2PInvalidParams {
		t.Fatalf("expected code %d got %d", codeP2PInvalidParams, rpcErr.Code)
	}
}

func TestP2PCreateAndGetTrade(t *testing.T) {
	env := newTestEnv(t)
	buyerKey, _ := crypto.GeneratePrivateKey()
	sellerKey, _ := crypto.GeneratePrivateKey()
	offerID := "OFF_SUCCESS"
	deadline := time.Now().Add(2 * time.Minute).Unix()
        payload := map[string]interface{}{
                "offerId":     offerID,
                "buyer":       buyerKey.PubKey().Address().String(),
                "seller":      sellerKey.PubKey().Address().String(),
                "baseToken":   "NHB",
                "baseAmount":  "5",
                "quoteToken":  "ZNHB",
                "quoteAmount": "7",
                "deadline":    deadline,
                "slippageBps": 75,
        }
	req := &RPCRequest{ID: 6, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleP2PCreateTrade(rec, env.newRequest(), req)
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("create error: %+v", rpcErr)
	}
	var createRes p2pCreateResult
	if err := json.Unmarshal(result, &createRes); err != nil {
		t.Fatalf("decode create result: %v", err)
	}
	if createRes.TradeID == "" {
		t.Fatalf("expected trade id")
	}
	if createRes.PayIntents == nil {
		t.Fatalf("expected pay intents")
	}
	buyerIntent, ok := createRes.PayIntents["buyer"]
	if !ok {
		t.Fatalf("missing buyer intent")
	}
	sellerIntent, ok := createRes.PayIntents["seller"]
	if !ok {
		t.Fatalf("missing seller intent")
	}
	buyerVault, err := env.node.EscrowVaultAddress("ZNHB")
	if err != nil {
		t.Fatalf("vault address: %v", err)
	}
	sellerVault, err := env.node.EscrowVaultAddress("NHB")
	if err != nil {
		t.Fatalf("vault address: %v", err)
	}
	expectedBuyerTo := crypto.NewAddress(crypto.NHBPrefix, buyerVault[:]).String()
	if buyerIntent.To != expectedBuyerTo {
		t.Fatalf("unexpected buyer intent to: got %s want %s", buyerIntent.To, expectedBuyerTo)
	}
	if buyerIntent.Token != "ZNHB" || buyerIntent.Amount != "7" {
		t.Fatalf("unexpected buyer intent payload: %+v", buyerIntent)
	}
	expectedSellerTo := crypto.NewAddress(crypto.NHBPrefix, sellerVault[:]).String()
	if sellerIntent.To != expectedSellerTo {
		t.Fatalf("unexpected seller intent to: got %s want %s", sellerIntent.To, expectedSellerTo)
	}
	if sellerIntent.Token != "NHB" || sellerIntent.Amount != "5" {
		t.Fatalf("unexpected seller intent payload: %+v", sellerIntent)
	}
	if buyerIntent.Memo != "ESCROW:"+createRes.EscrowQuoteID {
		t.Fatalf("unexpected buyer memo: %s", buyerIntent.Memo)
	}
	if sellerIntent.Memo != "ESCROW:"+createRes.EscrowBaseID {
		t.Fatalf("unexpected seller memo: %s", sellerIntent.Memo)
	}

	getPayload := map[string]string{"tradeId": createRes.TradeID}
	getReq := &RPCRequest{ID: 7, Params: []json.RawMessage{marshalParam(t, getPayload)}}
	getRec := httptest.NewRecorder()
	env.server.handleP2PGetTrade(getRec, env.newRequest(), getReq)
	tradeResult, getErr := decodeRPCResponse(t, getRec)
	if getErr != nil {
		t.Fatalf("get error: %+v", getErr)
	}
	var tradeRes tradeJSON
	if err := json.Unmarshal(tradeResult, &tradeRes); err != nil {
		t.Fatalf("decode trade: %v", err)
	}
	if tradeRes.OfferID != offerID {
		t.Fatalf("unexpected offer id: %s", tradeRes.OfferID)
	}
        if tradeRes.Status != "init" {
                t.Fatalf("unexpected status: %s", tradeRes.Status)
        }
        if tradeRes.QuoteAmount != "7" || tradeRes.BaseAmount != "5" {
                t.Fatalf("unexpected amounts: %+v", tradeRes)
        }
        if tradeRes.SlippageBps != 75 {
                t.Fatalf("unexpected slippage: %d", tradeRes.SlippageBps)
        }
}

func TestP2PSettleForbiddenCaller(t *testing.T) {
	env := newTestEnv(t)
	buyerKey, _ := crypto.GeneratePrivateKey()
	sellerKey, _ := crypto.GeneratePrivateKey()
	outsiderKey, _ := crypto.GeneratePrivateKey()
	deadline := time.Now().Add(time.Minute).Unix()
	payload := map[string]interface{}{
		"offerId":     "OFF_FORBIDDEN",
		"buyer":       buyerKey.PubKey().Address().String(),
		"seller":      sellerKey.PubKey().Address().String(),
		"baseToken":   "NHB",
		"baseAmount":  "2",
		"quoteToken":  "ZNHB",
		"quoteAmount": "3",
		"deadline":    deadline,
	}
	createReq := &RPCRequest{ID: 8, Params: []json.RawMessage{marshalParam(t, payload)}}
	createRec := httptest.NewRecorder()
	env.server.handleP2PCreateTrade(createRec, env.newRequest(), createReq)
	createResult, createErr := decodeRPCResponse(t, createRec)
	if createErr != nil {
		t.Fatalf("create error: %+v", createErr)
	}
	var createRes p2pCreateResult
	if err := json.Unmarshal(createResult, &createRes); err != nil {
		t.Fatalf("decode create result: %v", err)
	}
	settlePayload := map[string]string{
		"tradeId": createRes.TradeID,
		"caller":  outsiderKey.PubKey().Address().String(),
	}
	settleReq := &RPCRequest{ID: 9, Params: []json.RawMessage{marshalParam(t, settlePayload)}}
	settleRec := httptest.NewRecorder()
	env.server.handleP2PSettle(settleRec, env.newRequest(), settleReq)
	_, settleErr := decodeRPCResponse(t, settleRec)
	if settleErr == nil {
		t.Fatalf("expected forbidden error")
	}
	if settleErr.Code != codeP2PForbidden {
		t.Fatalf("expected code %d got %d", codeP2PForbidden, settleErr.Code)
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
