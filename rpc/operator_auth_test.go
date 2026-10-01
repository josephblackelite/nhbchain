package rpc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"nhbchain/p2p"
)

// signEnvJWTWithRole mirrors signEnvJWT (loyalty_handlers_test.go) but
// additionally sets the 'role' claim, so a test can mint an operator-scoped
// (or deliberately wrong-role) credential against the same test JWT
// secret/issuer/audience that newTestEnv's server already trusts. An empty
// role omits the claim entirely, matching the shape of every credential
// minted today by generate_jwt.go, update_env.go and gov-keeper's mintJWT.
func signEnvJWTWithRole(t *testing.T, now time.Time, role string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss": "rpc-tests",
		"aud": jwt.ClaimStrings([]string{"unit-tests"}),
		"iat": now.Unix(),
		"nbf": now.Add(-time.Minute).Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	if role != "" {
		claims["role"] = role
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}

// netInfoRequest builds a real net_info JSON-RPC request body. net_info is
// used as the representative operator-only method for these tests because,
// unlike most of the table, its handler needs no node state at all (only
// s.net, see rpc/net_handlers.go), so a passing call is unambiguous proof
// the operator gate -- not some unrelated setup gap -- is what let it
// through (or blocked it).
func netInfoRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	body := buildRPCRequestBody(t, 1, "net_info", nil)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// (a) A request with no credential at all is rejected before it ever reaches
// the handler, with the ordinary "not authenticated" error -- net_info had
// NO auth check whatsoever before this change (see rpc/net_handlers.go: only
// net_dial/net_ban called requireAuthInto), so this also closes that gap.
func TestOperatorOnlyMethodRejectsMissingCredential(t *testing.T) {
	env := newTestEnv(t)
	env.server.net = &stubNetwork{}

	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, netInfoRequest(t, ""))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no credential, got %d: %s", rec.Code, rec.Body.String())
	}
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil || rpcErr.Code != codeUnauthorized {
		t.Fatalf("expected codeUnauthorized, got %+v", rpcErr)
	}
}

// (a) A non-operator credential is a VALID credential: env.token is a real,
// correctly-signed JWT for this server (same issuer/audience/secret), with
// no 'role' claim -- exactly the shape of every daemon credential minted by
// generate_jwt.go, update_env.go and gov-keeper's mintJWT today. It must
// still be rejected for an operator-only method, and with the distinct
// codeOperatorOnly error (not the generic unauthorized message), so a human
// or a daemon's own logs can tell "bad credential" apart from "credential
// fine, wrong role".
func TestOperatorOnlyMethodRejectsNonOperatorCredential(t *testing.T) {
	env := newTestEnv(t)
	env.server.net = &stubNetwork{}

	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, netInfoRequest(t, env.token))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a valid-but-non-operator credential, got %d: %s", rec.Code, rec.Body.String())
	}
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil || rpcErr.Code != codeOperatorOnly {
		t.Fatalf("expected codeOperatorOnly, got %+v", rpcErr)
	}
	if rpcErr.Code == codeUnauthorized {
		t.Fatalf("a validly-authenticated non-operator must not get the generic unauthorized error")
	}
}

// A role claim present but not equal to "operator" (a typo, or a role value
// meant for some other system) must be treated exactly like no role claim at
// all -- never partially trusted.
func TestOperatorOnlyMethodRejectsWrongRoleValue(t *testing.T) {
	env := newTestEnv(t)
	env.server.net = &stubNetwork{}
	token := signEnvJWTWithRole(t, env.now, "admin")

	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, netInfoRequest(t, token))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for role=admin, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, rpcErr := decodeRPCResponse(t, rec); rpcErr == nil || rpcErr.Code != codeOperatorOnly {
		t.Fatalf("expected codeOperatorOnly for a non-operator role value, got %+v", rpcErr)
	}
}

// (b) An operator-role credential can still call an operator-only method --
// the handler actually runs (its result is checked, not just the status
// code) and the net_info case arm never calls requireAuthInto itself, so
// this failing would mean the centralized gate, not some per-case auth call,
// is what is under test.
func TestOperatorOnlyMethodAllowsOperatorCredential(t *testing.T) {
	env := newTestEnv(t)
	env.server.net = &stubNetwork{
		view: p2p.NetworkView{
			Self: p2p.NetworkSelf{NodeID: "operator-test-node"},
		},
	}
	token := signEnvJWTWithRole(t, env.now, "operator")

	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, netInfoRequest(t, token))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an operator credential, got %d: %s", rec.Code, rec.Body.String())
	}
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var info netInfoResult
	if err := json.Unmarshal(result, &info); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if info.NodeID != "operator-test-node" {
		t.Fatalf("expected the real handler to have run, got nodeId %q", info.NodeID)
	}
}

// Role matching is case-insensitive, same as every other role/claim
// comparison in this file (audience, issuer).
func TestOperatorOnlyMethodAllowsCaseInsensitiveRole(t *testing.T) {
	env := newTestEnv(t)
	env.server.net = &stubNetwork{}
	token := signEnvJWTWithRole(t, env.now, "Operator")

	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, netInfoRequest(t, token))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for role=Operator, got %d: %s", rec.Code, rec.Body.String())
	}
}

// (c) A method that is NOT in OperatorOnlyMethods must be completely
// unaffected by this change, for an anonymous caller, a non-operator
// credential, and an operator credential alike. nhb_getBalance is sent with
// zero params on purpose: its handler returns its own codeInvalidParams
// error before ever touching s.node (see rpc/http.go's
// handleGetBalance), so the assertion isolates "was this gated at all" from
// node/account setup.
func TestNonOperatorOnlyMethodUnaffectedByOperatorGate(t *testing.T) {
	env := newTestEnv(t)
	operatorToken := signEnvJWTWithRole(t, env.now, "operator")

	cases := []struct {
		name  string
		token string
	}{
		{"anonymous", ""},
		{"non-operator credential", env.token},
		{"operator credential", operatorToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := buildRPCRequestBody(t, 1, "nhb_getBalance", nil)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			env.server.ServeHTTP(rec, req)

			_, rpcErr := decodeRPCResponse(t, rec)
			if rpcErr == nil {
				t.Fatalf("expected the handler's own missing-params error")
			}
			if rpcErr.Code == codeUnauthorized || rpcErr.Code == codeOperatorOnly {
				t.Fatalf("non-operator-only method must not be gated by this change, got %+v", rpcErr)
			}
			if rpcErr.Code != codeInvalidParams {
				t.Fatalf("expected codeInvalidParams from the handler itself, got %+v", rpcErr)
			}
		})
	}
}

// (c, swap-partner carve-out) Methods authenticated through the separate
// per-partner HMAC swap API key (isPublicSwapMethod) must likewise be
// unaffected by the operator gate even though several of them are also in
// OperatorOnlyMethods -- see the exemption and its rationale in
// rpc/http.go's handle and rpc/operator_methods.go's package comment. This
// locks in the TestStableRPCHandlersFlow / TestSwapHandlersAcceptSignedRequest
// regression this change would otherwise cause for real swap partners.
func TestSwapPartnerMethodExemptFromOperatorGate(t *testing.T) {
	if !IsOperatorOnlyMethod("swap_submitVoucher") {
		t.Fatalf("test assumption broken: swap_submitVoucher should still be in OperatorOnlyMethods")
	}
	if !isPublicSwapMethod("swap_submitVoucher") {
		t.Fatalf("test assumption broken: swap_submitVoucher should still be in isPublicSwapMethod")
	}
}
