package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// potso_export_epoch is an operator-only method (rpc/operator_methods.go), so
// `nhb-cli potso reward export` must send the bearer token. It used to send no
// Authorization header at all and always got a 401 (PL-R5T-17).
func TestPotsoRewardExportSendsTheBearerToken(t *testing.T) {
	var gotAuth, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotMethod = req.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"epoch":3,"csv":""}}`))
	}))
	defer server.Close()

	originalEndpoint, originalToken := rpcEndpoint, rpcAuthToken
	rpcEndpoint, rpcAuthToken = server.URL, "operator-test-token"
	defer func() { rpcEndpoint, rpcAuthToken = originalEndpoint, originalToken }()

	var stdout, stderr bytes.Buffer
	_ = runPotsoRewardExport([]string{"--epoch", "3"}, &stdout, &stderr)
	if gotMethod != "potso_export_epoch" {
		t.Fatalf("the CLI called %q, want potso_export_epoch (stderr: %s)", gotMethod, stderr.String())
	}
	if gotAuth != "Bearer operator-test-token" {
		t.Fatalf("Authorization = %q, want the bearer token", gotAuth)
	}
}

// Without a token the CLI says so itself instead of sending a call that is
// certain to be refused.
func TestPotsoRewardExportWithoutATokenSaysSo(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()

	originalEndpoint, originalToken := rpcEndpoint, rpcAuthToken
	rpcEndpoint, rpcAuthToken = server.URL, ""
	defer func() { rpcEndpoint, rpcAuthToken = originalEndpoint, originalToken }()

	var stdout, stderr bytes.Buffer
	if code := runPotsoRewardExport([]string{"--epoch", "3"}, &stdout, &stderr); code == 0 {
		t.Fatalf("export without a token reported success")
	}
	if called {
		t.Fatalf("a call that needs a token was sent without one")
	}
	if !strings.Contains(stderr.String(), "NHB_RPC_TOKEN") {
		t.Fatalf("the message must name NHB_RPC_TOKEN, got %q", stderr.String())
	}
}
