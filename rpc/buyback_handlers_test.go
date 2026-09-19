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

// refPriceStatusResult is the wire shape buyback_getRefPriceStatus returns.
type refPriceStatusResult struct {
	Epoch       uint64 `json:"epoch"`
	HasRefPrice bool   `json:"hasRefPrice"`
}

// callRefPriceStatus issues a real buyback_getRefPriceStatus JSON-RPC call
// through the server's dispatch. A nil epoch is the "current open epoch"
// form (no params at all); otherwise the explicit-epoch object form.
func callRefPriceStatus(t *testing.T, env *testEnv, epoch *uint64) refPriceStatusResult {
	t.Helper()
	var params []json.RawMessage
	if epoch != nil {
		params = []json.RawMessage{marshalParam(t, map[string]uint64{"epoch": *epoch})}
	}
	body := buildRPCRequestBody(t, 1, "buyback_getRefPriceStatus", params)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}
	var status refPriceStatusResult
	if err := json.Unmarshal(result, &status); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return status
}

// TestHandleBuybackGetRefPriceStatusReportsNextBlockEpochAtBoundary pins
// down which epoch "the current open epoch" means: the epoch the NEXT block
// (latest committed height + 1) will be evaluated in -- the same height
// applyBuybackRefPrice checks a submission against -- not the epoch of the
// latest committed block. With an epoch length of 100, epoch k covers
// heights 100(k-1)+1 .. 100k, so once height 100k is committed the next
// block (100k+1) is already in epoch k+1, and a submitter that were told
// "epoch k" there would build a transaction that can never apply.
func TestHandleBuybackGetRefPriceStatusReportsNextBlockEpochAtBoundary(t *testing.T) {
	const epochLength = 100
	env, _, _ := newBuybackTestEnv(t)
	if err := env.node.ConfigureEpochLengthForTests(epochLength); err != nil {
		t.Fatalf("configure epoch length: %v", err)
	}

	advanceTo := func(target uint64) {
		t.Helper()
		for env.node.GetHeight() < target {
			block, err := env.node.CreateBlock(nil)
			if err != nil {
				t.Fatalf("create block %d: %v", env.node.GetHeight()+1, err)
			}
			if err := env.node.CommitBlock(block); err != nil {
				t.Fatalf("commit block %d: %v", block.Header.Height, err)
			}
		}
	}

	// Latest committed 100k-1 => the next block IS 100k => still epoch k.
	// Latest committed 100k   => the next block is 100k+1 => epoch k+1.
	checkpoints := []struct {
		latestCommitted uint64
		wantEpoch       uint64
	}{
		{latestCommitted: 1*epochLength - 1, wantEpoch: 1},
		{latestCommitted: 1 * epochLength, wantEpoch: 2},
		{latestCommitted: 2*epochLength - 1, wantEpoch: 2},
		{latestCommitted: 2 * epochLength, wantEpoch: 3},
	}
	for _, cp := range checkpoints {
		advanceTo(cp.latestCommitted)
		if got := env.node.GetHeight(); got != cp.latestCommitted {
			t.Fatalf("test setup: latest committed height = %d, want %d", got, cp.latestCommitted)
		}
		status := callRefPriceStatus(t, env, nil)
		if status.Epoch != cp.wantEpoch {
			t.Fatalf("latest committed height %d: reported current epoch %d, want %d (the epoch block %d will be evaluated in)",
				cp.latestCommitted, status.Epoch, cp.wantEpoch, cp.latestCommitted+1)
		}
	}
}

// TestHandleBuybackGetRefPriceStatusHasRefPriceTracksReportedEpoch proves
// HasRefPrice in the "current open epoch" form refers to the SAME epoch the
// response names, across the exact boundary where the reported epoch rolls
// over: after the last block of epoch 1 commits with epoch 1's price in it,
// the default query moves on to epoch 2 (no price yet) while an explicit
// epoch-1 query still shows the recorded price. Explicit-epoch queries are
// unaffected by the height.
func TestHandleBuybackGetRefPriceStatusHasRefPriceTracksReportedEpoch(t *testing.T) {
	const epochLength = 100
	env, keys, _ := newBuybackTestEnv(t)
	if err := env.node.ConfigureEpochLengthForTests(epochLength); err != nil {
		t.Fatalf("configure epoch length: %v", err)
	}
	for env.node.GetHeight() < epochLength-1 {
		block, err := env.node.CreateBlock(nil)
		if err != nil {
			t.Fatalf("create block %d: %v", env.node.GetHeight()+1, err)
		}
		if err := env.node.CommitBlock(block); err != nil {
			t.Fatalf("commit block %d: %v", block.Header.Height, err)
		}
	}

	// Next block is 100, the last block of epoch 1.
	before := callRefPriceStatus(t, env, nil)
	if before.Epoch != 1 || before.HasRefPrice {
		t.Fatalf("before submission: got %+v, want epoch 1 without a ref price", before)
	}

	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(time.Now().UTC().Unix())
	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Epoch:     before.Epoch,
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	sigs := [][]byte{signBuybackRefPrice(t, keys[0], rp), signBuybackRefPrice(t, keys[1], rp)}
	if _, err := env.node.SubmitBuybackRefPrice(rateNum, rateDenom, before.Epoch, ts, sigs); err != nil {
		t.Fatalf("submit ref price for the reported epoch: %v", err)
	}
	block, err := env.node.CreateBlock(env.node.GetMempool())
	if err != nil {
		t.Fatalf("create block %d: %v", env.node.GetHeight()+1, err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected the ref price in block %d, got %d txs", block.Header.Height, len(block.Transactions))
	}
	if err := env.node.CommitBlock(block); err != nil {
		t.Fatalf("commit block %d: %v", block.Header.Height, err)
	}

	// Latest committed is 100 => next block 101 => epoch 2, no price yet.
	current := callRefPriceStatus(t, env, nil)
	if current.Epoch != 2 || current.HasRefPrice {
		t.Fatalf("after the boundary block: got %+v, want epoch 2 without a ref price", current)
	}
	one, two := uint64(1), uint64(2)
	if got := callRefPriceStatus(t, env, &one); got.Epoch != 1 || !got.HasRefPrice {
		t.Fatalf("explicit epoch 1: got %+v, want the recorded price", got)
	}
	if got := callRefPriceStatus(t, env, &two); got.Epoch != 2 || got.HasRefPrice {
		t.Fatalf("explicit epoch 2: got %+v, want no price", got)
	}
}
