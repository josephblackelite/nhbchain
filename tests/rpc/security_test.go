package rpc_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
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
	srv := rpc.NewServer(nil, nil, rpc.ServerConfig{})
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
	srv := rpc.NewServer(nil, nil, cfg)
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
}

func TestRequireAuthWithMTLS(t *testing.T) {
	srv := rpc.NewServer(nil, nil, rpc.ServerConfig{TLSClientCAFile: "dummy-ca.pem"})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.TLS = &tls.ConnectionState{
		VerifiedChains:    [][]*x509.Certificate{{{}}},
		HandshakeComplete: true,
	}
	if err := srv.TestRequireAuth(req); err != nil {
		t.Fatalf("expected client certificate to satisfy auth: %v", err)
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
	srv := rpc.NewServer(nil, nil, cfg)

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
