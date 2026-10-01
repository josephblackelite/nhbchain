package rpc

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/tokenomics/lendingoracle"
)

// (a) buyback_submitRefPrice is in OperatorOnlyMethods but is exempt from the
// operator-ROLE requirement (isSignatureThresholdAuthorizedMethod in
// rpc/http.go). Unlike mint_with_sig, its case arm still calls
// requireAuthInto -- that pre-existing check (present since before this
// branch, see TestHandleBuybackSubmitRefPriceRequiresAuth) still requires a
// validly-authenticated credential. What this test proves is the narrower
// claim: a credential that authenticates successfully but carries no
// 'operator' role claim -- exactly the shape of every credential minted
// today by generate_jwt.go, update_env.go and gov-keeper's mintJWT, which is
// what a real, currently-deployed off-chain reference-price submission
// service holds -- must still be accepted here, exactly as it was before the
// operator-role gate existed on this branch at all. Before this exemption,
// the same request would have been rejected with codeOperatorOnly (403)
// once the operator gate landed, which would have broken that caller on
// deploy.
func TestBuybackSubmitRefPriceExemptAllowsNonOperatorCredential(t *testing.T) {
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
	sigs := [][]byte{signBuybackRefPrice(t, keys[0], rp), signBuybackRefPrice(t, keys[1], rp)}
	payload := buybackRefPricePayload(rateNum, rateDenom, epochNum, ts, sigs)
	body := buildRPCRequestBody(t, 1, "buyback_submitRefPrice", []json.RawMessage{marshalParam(t, payload)})

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	// env.token is a validly-signed JWT for this server with NO 'role'
	// claim -- a non-operator credential, not an anonymous caller.
	req.Header.Set("Authorization", "Bearer "+env.token)
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a non-operator but authenticated credential, got %d: %s", rec.Code, rec.Body.String())
	}
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error (operator-role gate should not apply): %+v", rpcErr)
	}
	var out struct {
		TxHash string `json:"txHash"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if out.TxHash == "" {
		t.Fatalf("expected a tx hash, got empty result: %s", result)
	}
}

// (b) Same as above, for lending_submitRefPrice.
func TestLendingSubmitRefPriceExemptAllowsNonOperatorCredential(t *testing.T) {
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

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a non-operator but authenticated credential, got %d: %s", rec.Code, rec.Body.String())
	}
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error (operator-role gate should not apply): %+v", rpcErr)
	}
	var out struct {
		TxHash string `json:"txHash"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if out.TxHash == "" {
		t.Fatalf("expected a tx hash, got empty result: %s", result)
	}
}

// (c) The exemption must not weaken or bypass the pre-existing requireAuthInto
// check: a request with NO credential at all must still be rejected with the
// ordinary codeUnauthorized error, not let through because the method is
// exempt from the operator-role layer. This is the key difference from
// mint_with_sig's exemption (which genuinely allows zero credential) that
// the "fresh investigation" behind this change must not blur: these two
// methods were never, and are still not, callable with no credential.
func TestBuybackLendingRefPriceExemptionDoesNotBypassBaseAuth(t *testing.T) {
	if !isSignatureThresholdAuthorizedMethod("buyback_submitRefPrice") {
		t.Fatalf("test assumption broken: buyback_submitRefPrice should be exempt from the operator-role gate")
	}
	if !isSignatureThresholdAuthorizedMethod("lending_submitRefPrice") {
		t.Fatalf("test assumption broken: lending_submitRefPrice should be exempt from the operator-role gate")
	}

	cases := []struct {
		name   string
		env    func(t *testing.T) *testEnv
		method string
		params func(t *testing.T, env *testEnv) []json.RawMessage
	}{
		{
			name:   "buyback_submitRefPrice",
			method: "buyback_submitRefPrice",
			env:    func(t *testing.T) *testEnv { env, _, _ := newBuybackTestEnv(t); return env },
			params: func(t *testing.T, env *testEnv) []json.RawMessage {
				epochNum, ok := env.node.CurrentBuybackEpoch()
				if !ok {
					t.Fatalf("expected epoch scheduling to be enabled")
				}
				rateNum, rateDenom := big.NewInt(5), big.NewInt(100)
				ts := uint64(time.Now().UTC().Unix())
				payload := buybackRefPricePayload(rateNum, rateDenom, epochNum, ts, nil)
				return []json.RawMessage{marshalParam(t, payload)}
			},
		},
		{
			name:   "lending_submitRefPrice",
			method: "lending_submitRefPrice",
			env:    func(t *testing.T) *testEnv { env, _, _ := newLendingRefPriceTestEnv(t); return env },
			params: func(t *testing.T, env *testEnv) []json.RawMessage {
				rateNum, rateDenom := big.NewInt(5), big.NewInt(100)
				ts := uint64(time.Now().UTC().Unix())
				payload := lendingRefPriceRPCPayload(rateNum, rateDenom, ts, nil)
				return []json.RawMessage{marshalParam(t, payload)}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.env(t)
			body := buildRPCRequestBody(t, 1, tc.method, tc.params(t, env))
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			// Deliberately no Authorization header at all.
			rec := httptest.NewRecorder()
			env.server.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 (base auth still required) for %s with no credential, got %d: %s", tc.method, rec.Code, rec.Body.String())
			}
			_, rpcErr := decodeRPCResponse(t, rec)
			if rpcErr == nil || rpcErr.Code != codeUnauthorized {
				t.Fatalf("expected codeUnauthorized for %s with no credential, got %+v", tc.method, rpcErr)
			}
		})
	}
}

// (c, continued) An insufficient/invalid signature bundle must still be
// rejected even with a valid non-operator credential -- the exemption only
// touches the operator-role layer, never the on-chain signature-threshold
// check, in either direction.
func TestBuybackLendingRefPriceExemptionDoesNotWeakenSignatureThresholdCheck(t *testing.T) {
	t.Run("buyback_submitRefPrice", func(t *testing.T) {
		env, keys, _ := newBuybackTestEnv(t)
		epochNum, ok := env.node.CurrentBuybackEpoch()
		if !ok {
			t.Fatalf("expected epoch scheduling to be enabled")
		}
		rateNum, rateDenom := big.NewInt(5), big.NewInt(100)
		ts := uint64(time.Now().UTC().Unix())
		rp := &buyback.ReferencePrice{Rate: new(big.Rat).SetFrac(rateNum, rateDenom), Epoch: epochNum, Timestamp: time.Unix(int64(ts), 0).UTC()}
		// Threshold is 2; only one of three signers signs.
		sigs := [][]byte{signBuybackRefPrice(t, keys[0], rp)}
		payload := buybackRefPricePayload(rateNum, rateDenom, epochNum, ts, sigs)
		body := buildRPCRequestBody(t, 1, "buyback_submitRefPrice", []json.RawMessage{marshalParam(t, payload)})
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+env.token)
		rec := httptest.NewRecorder()
		env.server.ServeHTTP(rec, req)
		_, rpcErr := decodeRPCResponse(t, rec)
		if rpcErr == nil {
			t.Fatalf("expected an error for a below-threshold signature bundle")
		}
		if rpcErr.Code == codeOperatorOnly {
			t.Fatalf("a below-threshold signature bundle must fail with the signature error, not the operator-role gate: %+v", rpcErr)
		}
	})

	t.Run("lending_submitRefPrice", func(t *testing.T) {
		env, keys, _ := newLendingRefPriceTestEnv(t)
		rateNum, rateDenom := big.NewInt(5), big.NewInt(100)
		ts := uint64(time.Now().UTC().Unix())
		rp := &lendingoracle.ReferencePrice{Rate: new(big.Rat).SetFrac(rateNum, rateDenom), Timestamp: time.Unix(int64(ts), 0).UTC()}
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
		if rpcErr.Code == codeOperatorOnly {
			t.Fatalf("a below-threshold signature bundle must fail with the signature error, not the operator-role gate: %+v", rpcErr)
		}
	})
}

// (d) The new exemption must be narrowly scoped to these two methods: nearby
// operator-only methods not in any exemption (swap_setManualQuote, net_ban)
// must remain gated exactly as before, with no credential and with a valid
// non-operator credential alike.
func TestRefPriceExemptionDoesNotWidenToOtherOperatorMethods(t *testing.T) {
	for _, method := range []string{"swap_setManualQuote", "net_ban"} {
		if isSignatureThresholdAuthorizedMethod(method) {
			t.Fatalf("isSignatureThresholdAuthorizedMethod must not claim %q", method)
		}
		if isPublicSwapMethod(method) {
			t.Fatalf("test assumption broken: %q should not be in isPublicSwapMethod", method)
		}
		if isSelfAuthenticatedMintMethod(method) {
			t.Fatalf("test assumption broken: %q should not be in isSelfAuthenticatedMintMethod", method)
		}
		if !IsOperatorOnlyMethod(method) {
			t.Fatalf("test assumption broken: %q should still be in OperatorOnlyMethods", method)
		}
	}

	env := newTestEnv(t)
	env.server.net = &stubNetwork{}

	for _, method := range []string{"swap_setManualQuote", "net_ban"} {
		t.Run(method+"/no credential", func(t *testing.T) {
			body := buildRPCRequestBody(t, 1, method, nil)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			env.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 (still gated) for %s with no credential, got %d: %s", method, rec.Code, rec.Body.String())
			}
			if _, rpcErr := decodeRPCResponse(t, rec); rpcErr == nil || rpcErr.Code != codeUnauthorized {
				t.Fatalf("expected codeUnauthorized for %s, got %+v", method, rpcErr)
			}
		})
		t.Run(method+"/non-operator credential", func(t *testing.T) {
			body := buildRPCRequestBody(t, 1, method, nil)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+env.token)
			rec := httptest.NewRecorder()
			env.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 (still gated) for %s with a non-operator credential, got %d: %s", method, rec.Code, rec.Body.String())
			}
			if _, rpcErr := decodeRPCResponse(t, rec); rpcErr == nil || rpcErr.Code != codeOperatorOnly {
				t.Fatalf("expected codeOperatorOnly for %s, got %+v", method, rpcErr)
			}
		})
	}
}
