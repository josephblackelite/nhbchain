package rpc_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	gatewayauth "nhbchain/gateway/auth"
	"nhbchain/rpc"
)

func TestRPCRejectsPlaintextWithoutAllowInsecure(t *testing.T) {
	t.Setenv("TEST_RPC_JWT_SECRET", "reject-plaintext")
	srv, err := rpc.NewServer(nil, nil, rpc.ServerConfig{
		JWT: rpc.JWTConfig{
			Enable:      true,
			Alg:         "HS256",
			HSSecretEnv: "TEST_RPC_JWT_SECRET",
			Issuer:      "rpc-suite",
			Audience:    []string{"tests"},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	if err := srv.Serve(listener); err == nil || !strings.Contains(err.Error(), "TLS is required") {
		t.Fatalf("expected TLS requirement error, got %v", err)
	}
}

func TestRequireAuthWithJWT(t *testing.T) {
	t.Setenv("TEST_RPC_JWT_SECRET", "super-secret")
	cfg := rpc.ServerConfig{
		JWT: rpc.JWTConfig{
			Enable:         true,
			Alg:            "HS256",
			HSSecretEnv:    "TEST_RPC_JWT_SECRET",
			Issuer:         "rpc-service",
			Audience:       []string{"wallets"},
			MaxSkewSeconds: 60,
		},
	}
	srv, err := rpc.NewServer(nil, nil, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	fixed := time.Now().UTC()
	secret := []byte("super-secret")
	valid := signJWT(t, secret, "rpc-service", []string{"wallets"}, fixed.Add(time.Hour), fixed.Add(-time.Minute))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+valid)
	if err := srv.TestRequireAuth(req); err != nil {
		t.Fatalf("expected JWT to authenticate: %v", err)
	}

	wrongIssuer := signJWT(t, secret, "other-service", []string{"wallets"}, fixed.Add(time.Hour), fixed.Add(-time.Minute))
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+wrongIssuer)
	if err := srv.TestRequireAuth(req); err == nil || err.Message != "invalid JWT" {
		t.Fatalf("expected issuer mismatch to fail, got %v", err)
	}

	wrongAudience := signJWT(t, secret, "rpc-service", []string{"ops"}, fixed.Add(time.Hour), fixed.Add(-time.Minute))
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+wrongAudience)
	if err := srv.TestRequireAuth(req); err == nil || err.Message != "invalid JWT" {
		t.Fatalf("expected audience mismatch to fail, got %v", err)
	}

	expired := signJWT(t, secret, "rpc-service", []string{"wallets"}, fixed.Add(-time.Minute), fixed.Add(-2*time.Minute))
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+expired)
	if err := srv.TestRequireAuth(req); err == nil || err.Message != "invalid JWT" {
		t.Fatalf("expected expired token to fail, got %v", err)
	}

	invalidSig := signJWT(t, []byte("other-secret"), "rpc-service", []string{"wallets"}, fixed.Add(time.Hour), fixed.Add(-time.Minute))
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+invalidSig)
	if err := srv.TestRequireAuth(req); err == nil || err.Message != "invalid JWT" {
		t.Fatalf("expected invalid signature to fail, got %v", err)
	}
}

func TestRequireAuthWithMTLS(t *testing.T) {
	srv, err := rpc.NewServer(nil, nil, rpc.ServerConfig{TLSClientCAFile: "dummy-ca.pem"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.TLS = &tls.ConnectionState{
		VerifiedChains:    [][]*x509.Certificate{{{}}},
		HandshakeComplete: true,
	}
	if err := srv.TestRequireAuth(req); err != nil {
		t.Fatalf("expected client certificate to satisfy auth: %v", err)
	}
}

func TestNewServerRejectsMissingJWTWithoutMTLS(t *testing.T) {
	if _, err := rpc.NewServer(nil, nil, rpc.ServerConfig{}); err == nil {
		t.Fatalf("expected missing JWT configuration to fail")
	}
}

func TestStableRPCMethodsRequireBearerToken(t *testing.T) {
	base := time.Unix(1717787717, 0).UTC()
	const codeUnauthorized = -32001
	t.Setenv("TEST_RPC_JWT_SECRET", "integration-secret")
	cfg := rpc.ServerConfig{
		JWT: rpc.JWTConfig{
			Enable:      true,
			Alg:         "HS256",
			HSSecretEnv: "TEST_RPC_JWT_SECRET",
			Issuer:      "rpc-service",
			Audience:    []string{"integration-tests"},
		},
		SwapAuth: rpc.SwapAuthConfig{
			Secrets:              map[string]string{"partner": "secret"},
			AllowedTimestampSkew: time.Minute,
			NonceTTL:             5 * time.Minute,
			NonceCapacity:        32,
			RateLimitWindow:      time.Minute,
			Now: func() time.Time {
				return base
			},
		},
	}
	srv, err := rpc.NewServer(nil, nil, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	type testCase struct {
		name   string
		method string
		params map[string]any
	}

	tests := []testCase{
		{
			name:   "request approval",
			method: "nhb_requestSwapApproval",
			params: map[string]any{"asset": "ZNHB", "amount": 100, "account": "merchant-123"},
		},
		{
			name:   "swap mint",
			method: "nhb_swapMint",
			params: map[string]any{"quoteId": "q-1717787718000000000", "amountIn": 100, "account": "merchant-123"},
		},
		{
			name:   "swap burn",
			method: "nhb_swapBurn",
			params: map[string]any{"reservationId": "q-1717787718000000000"},
		},
		{
			name:   "swap status",
			method: "nhb_getSwapStatus",
			params: nil,
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{
				"jsonrpc": "2.0",
				"id":      i + 1,
				"method":  tc.method,
			}
			if tc.params != nil {
				payload["params"] = []any{tc.params}
			} else {
				payload["params"] = []any{}
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			timestamp := strconv.FormatInt(base.Add(time.Duration(i)*time.Second).Unix(), 10)
			nonce := "integration-nonce-" + strconv.Itoa(i)
			req.Header.Set(gatewayauth.HeaderAPIKey, "partner")
			req.Header.Set(gatewayauth.HeaderTimestamp, timestamp)
			req.Header.Set(gatewayauth.HeaderNonce, nonce)
			signature := gatewayauth.ComputeSignature("secret", timestamp, nonce, req.Method, gatewayauth.CanonicalRequestPath(req), body)
			req.Header.Set(gatewayauth.HeaderSignature, hex.EncodeToString(signature))

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected unauthorized, got %d body %s", rec.Code, rec.Body.String())
			}

			var resp rpc.RPCResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if resp.Error == nil {
				t.Fatalf("expected RPC error response")
			}
			if resp.Error.Code != codeUnauthorized {
				t.Fatalf("unexpected error code %d", resp.Error.Code)
			}
		})
	}
}

func TestSwapHMACEnforcesSkewAndNonceTTL(t *testing.T) {
	now := time.Now().UTC()
	current := now
	cfg := rpc.ServerConfig{
		SwapAuth: rpc.SwapAuthConfig{
			Secrets:              map[string]string{"partner": "super-secret"},
			AllowedTimestampSkew: time.Second,
			NonceTTL:             2 * time.Second,
			NonceCapacity:        8,
			Now: func() time.Time {
				return current
			},
		},
	}
	t.Setenv("TEST_RPC_JWT_SECRET", "swap-auth-secret")
	cfg.JWT = rpc.JWTConfig{
		Enable:      true,
		Alg:         "HS256",
		HSSecretEnv: "TEST_RPC_JWT_SECRET",
		Issuer:      "rpc-service",
		Audience:    []string{"swap-tests"},
	}
	srv, err := rpc.NewServer(nil, nil, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sign := func(ts time.Time, nonce string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "https://example.com/swap", nil)
		req.Header.Set(gatewayauth.HeaderAPIKey, "partner")
		req.Header.Set(gatewayauth.HeaderTimestamp, strconv.FormatInt(ts.Unix(), 10))
		req.Header.Set(gatewayauth.HeaderNonce, nonce)
		sig := gatewayauth.ComputeSignature("super-secret", req.Header.Get(gatewayauth.HeaderTimestamp), nonce, req.Method, req.URL.Path, nil)
		req.Header.Set(gatewayauth.HeaderSignature, hex.EncodeToString(sig))
		return req
	}

	first := sign(now, "nonce-1")
	if _, err := srv.TestAuthenticateSwap(first, nil); err != nil {
		t.Fatalf("expected swap authentication to pass: %v", err)
	}

	if _, err := srv.TestAuthenticateSwap(first, nil); err == nil || !strings.Contains(err.Error(), "nonce already used") {
		t.Fatalf("expected nonce replay to be rejected, got %v", err)
	}

	current = current.Add(3 * time.Second)
	second := sign(current, "nonce-1")
	if _, err := srv.TestAuthenticateSwap(second, nil); err != nil {
		t.Fatalf("expected nonce reuse after ttl to succeed: %v", err)
	}

	stale := sign(current.Add(-3*time.Second), "nonce-2")
	if _, err := srv.TestAuthenticateSwap(stale, nil); err == nil || !strings.Contains(err.Error(), "timestamp outside allowed skew") {
		t.Fatalf("expected timestamp skew rejection, got %v", err)
	}
}

func signJWT(t *testing.T, secret []byte, issuer string, audience []string, exp, nbf time.Time) string {
	t.Helper()
	claims := jwt.RegisteredClaims{
		Issuer:    issuer,
		Audience:  jwt.ClaimStrings(audience),
		ExpiresAt: jwt.NewNumericDate(exp),
		NotBefore: jwt.NewNumericDate(nbf),
		IssuedAt:  jwt.NewNumericDate(nbf),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}
