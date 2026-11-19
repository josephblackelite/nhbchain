package rpc

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// PL-R5T-84: nhb_swapMint, nhb_swapBurn and swap_voucher_get/list/export are exempt from
// the operator gate because the per-partner HMAC check is the stronger control; that check
// runs only when partner secrets are configured. With the setting on and no secrets they
// need the operator role.

func swapGateServer(t *testing.T, require bool) (*Server, time.Time) {
	t.Helper()
	db := storage.NewMemDB()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	node, err := core.NewNode(db, key, "", true, true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	now := time.Unix(1700000000, 0).UTC()
	srv := newTestServer(t, node, nil, ServerConfig{SwapOperatorNoHMAC: require})
	if srv.jwtVerifier != nil {
		srv.jwtVerifier.now = func() time.Time { return now }
	}
	return srv, now
}

func swapGateCall(t *testing.T, srv *Server, method, token string) *httptest.ResponseRecorder {
	t.Helper()
	body := buildRPCRequestBody(t, 1, method, nil)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// gated methods: in the operator table, in the swap exemption list, and not self-authenticated.
var swapGatedMethods = []string{"nhb_swapMint", "nhb_swapBurn", "swap_voucher_get", "swap_voucher_list", "swap_voucher_export"}

func TestSwapMethodsWithTheSettingOffStayOpenToAnyValidToken(t *testing.T) {
	srv, now := swapGateServer(t, false)
	token := signEnvJWTWithRole(t, now, "")
	for _, method := range swapGatedMethods {
		rec := swapGateCall(t, srv, method, token)
		_, rpcErr := decodeRPCResponse(t, rec)
		if rpcErr != nil && rpcErr.Code == codeOperatorOnly {
			t.Fatalf("%s: setting off but the operator gate answered: %+v", method, rpcErr)
		}
		if rec.Code == http.StatusForbidden {
			t.Fatalf("%s: setting off but a non-operator token got 403", method)
		}
	}
}

func TestSwapMethodsWithTheSettingOnNeedTheOperatorRoleWithoutPartnerSecrets(t *testing.T) {
	srv, now := swapGateServer(t, true)
	plain := signEnvJWTWithRole(t, now, "")
	operator := signEnvJWTWithRole(t, now, "operator")
	for _, method := range swapGatedMethods {
		if !IsOperatorOnlyMethod(method) || !isPublicSwapMethod(method) {
			t.Fatalf("test assumption broken for %s", method)
		}
		rec := swapGateCall(t, srv, method, plain)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: a non-operator token got %d, want 403: %s", method, rec.Code, rec.Body.String())
		}
		if _, rpcErr := decodeRPCResponse(t, rec); rpcErr == nil || rpcErr.Code != codeOperatorOnly {
			t.Fatalf("%s: want the operator-only error, got %+v", method, rpcErr)
		}
		if rec := swapGateCall(t, srv, method, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: no credential got %d, want 401", method, rec.Code)
		}
		rec = swapGateCall(t, srv, method, operator)
		if _, rpcErr := decodeRPCResponse(t, rec); rpcErr != nil && rpcErr.Code == codeOperatorOnly {
			t.Fatalf("%s: an operator token was refused by the operator gate", method)
		}
	}
}

// swap_submitVoucher keeps its own carve-out (the on-chain voucher signature), and the
// methods that are not operator-only are untouched.
func TestSwapGateSettingLeavesTheOtherSwapMethodsAlone(t *testing.T) {
	srv, now := swapGateServer(t, true)
	plain := signEnvJWTWithRole(t, now, "")
	for _, method := range []string{"swap_submitVoucher", "nhb_getSwapStatus", "nhb_getSwapQuote", "swap_getRiskParams"} {
		rec := swapGateCall(t, srv, method, plain)
		if _, rpcErr := decodeRPCResponse(t, rec); rpcErr != nil && rpcErr.Code == codeOperatorOnly {
			t.Fatalf("%s: the operator gate answered a method it must not gate", method)
		}
		if rec.Code == http.StatusForbidden {
			t.Fatalf("%s: got 403", method)
		}
	}
}

// With partner secrets configured the HMAC check stands in for the operator role, setting
// or no setting: a partner's signed request with an ordinary (non-operator) token is not
// refused by the operator gate.
func TestSwapGateSettingWithPartnerSecretsKeepsTheExemption(t *testing.T) {
	env := newTestEnv(t)
	env.server.swapOperatorNoHMAC = true
	if env.server.swapAuth == nil {
		t.Fatalf("test assumption broken: the environment has partner secrets")
	}
	for i, method := range swapGatedMethods {
		body := buildRPCRequestBody(t, 1, method, nil)
		req := env.newRequest() // a plain bearer token, no operator role
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		env.signSwapRequest(t, req, body, "nonce-gate-"+strconv.Itoa(i))
		rec := httptest.NewRecorder()
		env.server.ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Fatalf("%s: the operator gate answered a partner-signed request: %s", method, rec.Body.String())
		}
		if _, rpcErr := decodeRPCResponse(t, rec); rpcErr != nil && rpcErr.Code == codeOperatorOnly {
			t.Fatalf("%s: the operator gate answered a partner-signed request", method)
		}
	}
}
