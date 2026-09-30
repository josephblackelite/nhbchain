package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhbchain/rpc"
)

const testRPCTokenSecret = "rpc-token-test-secret"

// newTokenVerifyingServer builds the real RPC server with the same [RPCJWT]
// settings the repository's config.toml ships, so a token accepted here is one
// the node accepts.
func newTokenVerifyingServer(t *testing.T, secret string) *rpc.Server {
	t.Helper()
	t.Setenv(rpcTokenSecretEnv, secret)
	srv, err := rpc.NewServer(nil, nil, rpc.ServerConfig{
		JWT: rpc.JWTConfig{
			Enable:         true,
			Alg:            "HS256",
			HSSecretEnv:    rpcTokenSecretEnv,
			Issuer:         "nhb-rpc",
			Audience:       []string{"wallets"},
			MaxSkewSeconds: 120,
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}

func authorize(srv *rpc.Server, token string) *rpc.RPCError {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return srv.TestRequireAuth(req)
}

func TestRPCTokenCommandMintsTokenTheNodeAccepts(t *testing.T) {
	srv := newTokenVerifyingServer(t, testRPCTokenSecret)

	var stdout, stderr bytes.Buffer
	if code := runRPCTokenCommand(nil, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("rpc-token exited %d: %s", code, stderr.String())
	}
	token := strings.TrimSpace(stdout.String())
	if token == "" || strings.Contains(token, testRPCTokenSecret) {
		t.Fatalf("expected a token that does not contain the secret, got %q", token)
	}
	if err := authorize(srv, token); err != nil {
		t.Fatalf("node rejected the minted token: %+v", err)
	}
}

func TestRPCTokenCommandReadsSecretFromStdin(t *testing.T) {
	srv := newTokenVerifyingServer(t, testRPCTokenSecret)
	t.Setenv(rpcTokenSecretEnv, "")

	var stdout, stderr bytes.Buffer
	code := runRPCTokenCommand([]string{"--secret-stdin"}, strings.NewReader(testRPCTokenSecret+"\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("rpc-token exited %d: %s", code, stderr.String())
	}
	if err := authorize(srv, strings.TrimSpace(stdout.String())); err != nil {
		t.Fatalf("node rejected the token minted from stdin: %+v", err)
	}
}

func TestRPCTokenCommandRequiresSecret(t *testing.T) {
	t.Setenv(rpcTokenSecretEnv, "  ")

	var stdout, stderr bytes.Buffer
	if code := runRPCTokenCommand(nil, strings.NewReader(""), &stdout, &stderr); code == 0 {
		t.Fatalf("expected a failure without a secret")
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected nothing on stdout, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), rpcTokenSecretEnv) {
		t.Fatalf("expected the error to name %s, got %q", rpcTokenSecretEnv, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runRPCTokenCommand([]string{"--secret-stdin"}, strings.NewReader("\n"), &stdout, &stderr); code == 0 {
		t.Fatalf("expected a failure with an empty secret on stdin")
	}
}

func TestRPCTokenIsRejectedForOtherSecretsAndWhenExpired(t *testing.T) {
	srv := newTokenVerifyingServer(t, testRPCTokenSecret)
	now := time.Now()

	other, err := mintRPCToken("some-other-secret", "nhb-rpc", []string{"wallets"}, time.Minute, now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if authorize(srv, other) == nil {
		t.Fatalf("expected a token signed with another secret to be rejected")
	}

	expired, err := mintRPCToken(testRPCTokenSecret, "nhb-rpc", []string{"wallets"}, time.Minute, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if authorize(srv, expired) == nil {
		t.Fatalf("expected an expired token to be rejected")
	}

	wrongIssuer, err := mintRPCToken(testRPCTokenSecret, "someone-else", []string{"wallets"}, time.Minute, now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if authorize(srv, wrongIssuer) == nil {
		t.Fatalf("expected a token from another issuer to be rejected")
	}
}

func TestRPCTokenLifetimeIsBounded(t *testing.T) {
	now := time.Now()
	for _, ttl := range []time.Duration{0, -time.Minute, maxRPCTokenTTL + time.Second} {
		if _, err := mintRPCToken(testRPCTokenSecret, "nhb-rpc", []string{"wallets"}, ttl, now); err == nil {
			t.Fatalf("expected ttl %s to be refused", ttl)
		}
	}
	if _, err := mintRPCToken(testRPCTokenSecret, "nhb-rpc", []string{"wallets"}, maxRPCTokenTTL, now); err != nil {
		t.Fatalf("expected the maximum ttl to be allowed: %v", err)
	}
}
