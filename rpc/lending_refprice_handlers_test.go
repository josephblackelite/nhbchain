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

	"nhbchain/core/tokenomics/lendingoracle"
	"nhbchain/crypto"
)

// newLendingRefPriceTestEnv mirrors newBuybackTestEnv (rpc/buyback_handlers_test.go):
// lending_submitRefPrice deliberately reuses the same genesis-declared
// buyback signer quorum rather than a second one (see
// core/lending_tx.go's applyLendingRefPriceTransaction doc comment), so the
// same ConfigureBuybackForTests call provisions both.
func newLendingRefPriceTestEnv(t *testing.T) (*testEnv, []*crypto.PrivateKey, [][20]byte) {
	t.Helper()
	return newBuybackTestEnv(t)
}

func signLendingRefPriceRPC(t *testing.T, key *crypto.PrivateKey, rp *lendingoracle.ReferencePrice) []byte {
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

func lendingRefPriceRPCPayload(rateNum, rateDenom *big.Int, ts uint64, sigs [][]byte) map[string]interface{} {
	sigHexes := make([]string, len(sigs))
	for i, sig := range sigs {
		sigHexes[i] = "0x" + hex.EncodeToString(sig)
	}
	return map[string]interface{}{
		"rateNum":    rateNum.String(),
		"rateDenom":  rateDenom.String(),
		"timestamp":  ts,
		"signatures": sigHexes,
	}
}

// TestHandleLendingSubmitRefPriceRequiresAuth mirrors
// TestHandleBuybackSubmitRefPriceRequiresAuth: lending_submitRefPrice's case
// arm in rpc/http.go's handle() has always called requireAuthInto, so a
// request with no credential at all must still be rejected before it ever
// reaches the handler -- unaffected by the operator-role exemption added
// alongside buyback_submitRefPrice's (isSignatureThresholdAuthorizedMethod
// only removes the additional operator-role layer, not this pre-existing
// check).
func TestHandleLendingSubmitRefPriceRequiresAuth(t *testing.T) {
	env, keys, _ := newLendingRefPriceTestEnv(t)
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(time.Now().UTC().Unix())
	rp := &lendingoracle.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	sigs := [][]byte{signLendingRefPriceRPC(t, keys[0], rp), signLendingRefPriceRPC(t, keys[1], rp)}
	payload := lendingRefPriceRPCPayload(rateNum, rateDenom, ts, sigs)
	body := buildRPCRequestBody(t, 1, "lending_submitRefPrice", []json.RawMessage{marshalParam(t, payload)})
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

// TestHandleLendingSubmitRefPriceEndToEnd exercises the real dispatch path
// (ServeHTTP, not the handler directly) with a valid non-operator credential
// and a threshold-satisfying signature bundle.
func TestHandleLendingSubmitRefPriceEndToEnd(t *testing.T) {
	env, keys, _ := newLendingRefPriceTestEnv(t)
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(time.Now().UTC().Unix())
	rp := &lendingoracle.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	sigs := [][]byte{signLendingRefPriceRPC(t, keys[0], rp), signLendingRefPriceRPC(t, keys[1], rp)}
	payload := lendingRefPriceRPCPayload(rateNum, rateDenom, ts, sigs)
	body := buildRPCRequestBody(t, 1, "lending_submitRefPrice", []json.RawMessage{marshalParam(t, payload)})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+env.token)
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	result, rpcErr := decodeRPCResponse(t, rec)
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

// TestHandleLendingSubmitRefPriceRejectsInsufficientSignatures mirrors
// TestHandleBuybackSubmitRefPriceRejectsInsufficientSignatures.
func TestHandleLendingSubmitRefPriceRejectsInsufficientSignatures(t *testing.T) {
	env, keys, _ := newLendingRefPriceTestEnv(t)
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(time.Now().UTC().Unix())
	rp := &lendingoracle.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	// Threshold is 2; only one of three signers signs.
	sigs := [][]byte{signLendingRefPriceRPC(t, keys[0], rp)}
	payload := lendingRefPriceRPCPayload(rateNum, rateDenom, ts, sigs)
	body := buildRPCRequestBody(t, 1, "lending_submitRefPrice", []json.RawMessage{marshalParam(t, payload)})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+env.token)
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected an error for a below-threshold signature bundle")
	}
}
