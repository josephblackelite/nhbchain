package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/tokenomics/buyback"
	"nhbchain/crypto"
)

func newBuybackTestEnv(t *testing.T) (*testEnv, []*crypto.PrivateKey, [][20]byte) {
	t.Helper()
	env := newTestEnv(t)
	if err := env.node.ConfigureEpochLengthForTests(2); err != nil {
		t.Fatalf("configure epoch length: %v", err)
	}
	keys := make([]*crypto.PrivateKey, 3)
	addrs := make([][20]byte, 3)
	for i := range keys {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate signer key %d: %v", i, err)
		}
		keys[i] = key
		copy(addrs[i][:], key.PubKey().Address().Bytes())
	}
	buybackCfg := buyback.Config{
		FeeShareBps:     2000,
		DiscountBps:     0,
		SafetyMarginBps: 0,
		SignerThreshold: 2,
		Signers:         addrs,
	}
	if err := env.node.ConfigureBuybackForTests(buybackCfg); err != nil {
		t.Fatalf("configure buyback: %v", err)
	}
	return env, keys, addrs
}

func signBuybackRefPrice(t *testing.T, key *crypto.PrivateKey, rp *buyback.ReferencePrice) []byte {
	t.Helper()
	digest, err := rp.Hash()
	if err != nil {
		t.Fatalf("hash reference price: %v", err)
	}
	sig, err := ethcrypto.Sign(digest[:], key.PrivateKey)
	if err != nil {
		t.Fatalf("sign reference price: %v", err)
	}
	return sig
}

func buybackRefPricePayload(rateNum, rateDenom *big.Int, epoch, ts uint64, sigs [][]byte) map[string]interface{} {
	sigHexes := make([]string, len(sigs))
	for i, sig := range sigs {
		sigHexes[i] = "0x" + hex.EncodeToString(sig)
	}
	return map[string]interface{}{
		"rateNum":    rateNum.String(),
		"rateDenom":  rateDenom.String(),
		"epoch":      epoch,
		"timestamp":  ts,
		"signatures": sigHexes,
	}
}

// TestHandleBuybackGetRefPriceStatusPublicNoAuth proves buyback_getRefPriceStatus
// is genuinely callable without any Authorization header, unlike
// buyback_submitRefPrice -- routed through the real dispatch (ServeHTTP),
// not the handler directly, so the auth gate (or lack of one) in
// rpc/http.go's method switch is what's actually being exercised.
func TestHandleBuybackGetRefPriceStatusPublicNoAuth(t *testing.T) {
	env, _, _ := newBuybackTestEnv(t)
	body := buildRPCRequestBody(t, 1, "buyback_getRefPriceStatus", nil)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with no auth header, got %d: %s", rec.Code, rec.Body.String())
	}
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}
	var status struct {
		Epoch       uint64 `json:"epoch"`
		HasRefPrice bool   `json:"hasRefPrice"`
	}
	if err := json.Unmarshal(result, &status); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if status.HasRefPrice {
		t.Fatalf("expected no reference price on file yet")
	}
	if status.Epoch == 0 {
		t.Fatalf("expected a positive current epoch")
	}
}

func TestHandleBuybackSubmitRefPriceRequiresAuth(t *testing.T) {
	env, keys, _ := newBuybackTestEnv(t)
	epochNum, ok := env.node.CurrentBuybackEpoch()
	if !ok {
		t.Fatalf("expected epoch scheduling to be enabled")
	}
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(time.Now().UTC().Unix())
	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Epoch:     epochNum,
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	sigs := [][]byte{
		signBuybackRefPrice(t, keys[0], rp),
		signBuybackRefPrice(t, keys[1], rp),
	}
	payload := buybackRefPricePayload(rateNum, rateDenom, epochNum, ts, sigs)
	body := buildRPCRequestBody(t, 1, "buyback_submitRefPrice", []json.RawMessage{marshalParam(t, payload)})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, rpcErr := decodeRPCResponse(t, rec); rpcErr == nil || rpcErr.Code != codeUnauthorized {
		t.Fatalf("expected unauthorized error, got %+v", rpcErr)
	}
}

func TestHandleBuybackSubmitRefPriceEndToEnd(t *testing.T) {
	env, keys, _ := newBuybackTestEnv(t)
	epochNum, ok := env.node.CurrentBuybackEpoch()
	if !ok {
		t.Fatalf("expected epoch scheduling to be enabled")
	}
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(time.Now().UTC().Unix())
	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Epoch:     epochNum,
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	sigs := [][]byte{
		signBuybackRefPrice(t, keys[0], rp),
		signBuybackRefPrice(t, keys[1], rp),
	}
	payload := buybackRefPricePayload(rateNum, rateDenom, epochNum, ts, sigs)
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleBuybackSubmitRefPrice(recorder, env.newRequest(), req)
	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}
	var resp struct {
		TxHash string `json:"txHash"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !strings.HasPrefix(resp.TxHash, "0x") {
		t.Fatalf("expected a 0x-prefixed tx hash, got %q", resp.TxHash)
	}
}

func TestHandleBuybackSubmitRefPriceRejectsInsufficientSignatures(t *testing.T) {
	env, keys, _ := newBuybackTestEnv(t)
	epochNum, ok := env.node.CurrentBuybackEpoch()
	if !ok {
		t.Fatalf("expected epoch scheduling to be enabled")
	}
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(time.Now().UTC().Unix())
	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Epoch:     epochNum,
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	// Threshold is 2; only one of three signers signs.
	sigs := [][]byte{signBuybackRefPrice(t, keys[0], rp)}
	payload := buybackRefPricePayload(rateNum, rateDenom, epochNum, ts, sigs)
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleBuybackSubmitRefPrice(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected an error for a below-threshold signature bundle")
	}
}
