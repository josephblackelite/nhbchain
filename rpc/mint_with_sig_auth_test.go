package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
)

// mintWithSigRequestBody builds a real mint_with_sig JSON-RPC request body,
// signing voucher with key exactly the way a real off-chain minting-service
// caller (and core/mint_test.go's signVoucher) does: keccak256 of the
// voucher's CanonicalJSON, secp256k1-signed.
func mintWithSigRequestBody(t *testing.T, id int, voucher core.MintVoucher, key *crypto.PrivateKey) []byte {
	t.Helper()
	payload, err := voucher.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	sig, err := ethcrypto.Sign(ethcrypto.Keccak256(payload), key.PrivateKey)
	if err != nil {
		t.Fatalf("sign voucher: %v", err)
	}
	params := []json.RawMessage{
		marshalParam(t, voucher),
		marshalParam(t, "0x"+hex.EncodeToString(sig)),
	}
	return buildRPCRequestBody(t, id, "mint_with_sig", params)
}

// assignMinterRole grants role (e.g. "MINTER_NHB") to key's address on
// env's node, mirroring core/mint_test.go's assignRole via the public
// WithState accessor (rpc-package tests cannot reach node.state directly).
func assignMinterRole(t *testing.T, env *testEnv, role string, key *crypto.PrivateKey) {
	t.Helper()
	addr := key.PubKey().Address().Bytes()
	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		return manager.SetRole(role, addr)
	}); err != nil {
		t.Fatalf("assign role %s: %v", role, err)
	}
}

// (a) mint_with_sig is in OperatorOnlyMethods but must be exempt from the
// operator-role gate (isSelfAuthenticatedMintMethod in rpc/http.go): a valid
// voucher signature from a MINTER_NHB-assigned key, with NO Authorization
// header at all, must still succeed exactly as it did before the operator
// gate existed -- handleMintWithSig (rpc/mint_handlers.go) never called
// requireAuthInto/requireOperatorInto itself; its only authorization was
// always the on-chain voucher signature + minter-role check inside
// core.Node.MintWithSignature / applyMintTransaction. The real, currently-
// deployed off-chain fiat-settlement minting service calls mint_with_sig
// with no bearer credential at all (docs/escrow/mint-settlement.md); if
// this regressed, every one of its calls would start failing with -32002
// (operator role required).
func TestMintWithSigExemptFromOperatorGateAllowsNoCredential(t *testing.T) {
	env := newTestEnv(t)

	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate minter key: %v", err)
	}
	assignMinterRole(t, env, "MINTER_NHB", minterKey)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	voucher := core.MintVoucher{
		InvoiceID: "inv-exempt-no-credential",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "100",
		ChainID:   core.MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	body := mintWithSigRequestBody(t, 1, voucher, minterKey)

	// Deliberately no Authorization header, no swap-partner HMAC headers --
	// reproducing the exact zero-auth-header shape the real caller sends.
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a valid voucher with no credential, got %d: %s", rec.Code, rec.Body.String())
	}
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error (operator gate should not apply): %+v", rpcErr)
	}
	var out struct {
		TxHash      string `json:"txHash"`
		VoucherHash string `json:"voucherHash"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if out.TxHash == "" {
		t.Fatalf("expected a tx hash, got empty result: %s", result)
	}
}

// (b) An invalid voucher signature (signed by a key that does NOT hold
// MINTER_NHB) must still be rejected -- unaffected by the operator-gate
// exemption in either direction. This proves the exemption only removes the
// operator-JWT requirement; it does not touch, weaken, or bypass
// MintWithSignature's own signer/role check.
func TestMintWithSigExemptionDoesNotWeakenVoucherSignatureCheck(t *testing.T) {
	env := newTestEnv(t)

	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate minter key: %v", err)
	}
	assignMinterRole(t, env, "MINTER_NHB", minterKey)

	rogueKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate rogue key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	voucher := core.MintVoucher{
		InvoiceID: "inv-exempt-bad-sig",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "100",
		ChainID:   core.MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	// Signed by rogueKey, which was never granted MINTER_NHB.
	body := mintWithSigRequestBody(t, 1, voucher, rogueKey)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an invalid voucher signer, got %d: %s", rec.Code, rec.Body.String())
	}
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected an rpc error for an invalid voucher signer")
	}
	if rpcErr.Code == codeOperatorOnly {
		t.Fatalf("invalid voucher signer must fail with the pre-existing signer error, not the operator gate: %+v", rpcErr)
	}
	if rpcErr.Code != codeUnauthorized {
		t.Fatalf("expected codeUnauthorized (ErrMintInvalidSigner), got %+v", rpcErr)
	}
}

// (c) The new exemption must be narrowly scoped to mint_with_sig: a nearby
// operator-only method in the same table section (swap_setManualQuote -- an
// operator-only swap-engine method that is deliberately NOT in
// isPublicSwapMethod's HMAC-exempt list either) must still be gated exactly
// as before. This guards against the exemption check accidentally widening
// (e.g. a typo'd prefix match) to cover methods it was never meant to.
func TestMintWithSigExemptionDoesNotWidenToOtherOperatorMethods(t *testing.T) {
	if isSelfAuthenticatedMintMethod("swap_setManualQuote") {
		t.Fatalf("isSelfAuthenticatedMintMethod must not claim swap_setManualQuote")
	}
	if isPublicSwapMethod("swap_setManualQuote") {
		t.Fatalf("test assumption broken: swap_setManualQuote should not be in isPublicSwapMethod either")
	}
	if !IsOperatorOnlyMethod("swap_setManualQuote") {
		t.Fatalf("test assumption broken: swap_setManualQuote should still be in OperatorOnlyMethods")
	}

	env := newTestEnv(t)
	body := buildRPCRequestBody(t, 1, "swap_setManualQuote", nil)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	// No credential at all, same as the mint_with_sig case above -- this
	// method must still be rejected by the operator gate.
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (still gated) for swap_setManualQuote with no credential, got %d: %s", rec.Code, rec.Body.String())
	}
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil || rpcErr.Code != codeUnauthorized {
		t.Fatalf("expected codeUnauthorized for a still-gated operator-only method, got %+v", rpcErr)
	}
}
