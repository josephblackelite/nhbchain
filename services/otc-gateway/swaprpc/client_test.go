package swaprpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	gatewayauth "nhbchain/gateway/auth"
)

func TestClientSignsRequests(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	nonce := "nonce-1"
	var capturedBody []byte
	var capturedHeaders http.Header
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		capturedBody = append([]byte(nil), body...)
		capturedHeaders = r.Header.Clone()
		capturedPath = gatewayauth.CanonicalRequestPath(r)
		resp := rpcResponse{JSONRPC: jsonRPCVersion, ID: 1, Result: json.RawMessage(`{"ok":true}`)}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		URL:               server.URL,
		Provider:          "test",
		APIKey:            "partner",
		APISecret:         "secret",
		AllowedMethods:    []string{"swap_voucher_list"},
		RequestsPerMinute: 10,
		Now:               func() time.Time { return now },
		Nonce:             func() (string, error) { return nonce, nil },
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	var out map[string]interface{}
	if err := client.call(context.Background(), "swap_voucher_list", []interface{}{int64(0), int64(0)}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}

	if got := capturedHeaders.Get(gatewayauth.HeaderAPIKey); got != "partner" {
		t.Fatalf("expected api key header, got %q", got)
	}
	ts := capturedHeaders.Get(gatewayauth.HeaderTimestamp)
	if ts == "" {
		t.Fatalf("missing timestamp header")
	}
	if ts != strconv.FormatInt(now.Unix(), 10) {
		t.Fatalf("unexpected timestamp %q", ts)
	}
	if gotNonce := capturedHeaders.Get(gatewayauth.HeaderNonce); gotNonce != nonce {
		t.Fatalf("unexpected nonce %q", gotNonce)
	}
	expectedSig := gatewayauth.ComputeSignature("secret", ts, nonce, http.MethodPost, capturedPath, capturedBody)
	if sig := capturedHeaders.Get(gatewayauth.HeaderSignature); sig != hex.EncodeToString(expectedSig) {
		t.Fatalf("unexpected signature %q", sig)
	}
}

func TestClientRejectsDisallowedMethod(t *testing.T) {
	client, err := NewClient(Config{
		URL:            "http://127.0.0.1",
		Provider:       "test",
		APIKey:         "partner",
		APISecret:      "secret",
		AllowedMethods: []string{"swap_voucher_list"},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := client.call(context.Background(), "swap_submitVoucher", nil, nil); err == nil {
		t.Fatalf("expected error for disallowed method")
	}
}

func TestClientRateLimiting(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	counter := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := rpcResponse{JSONRPC: jsonRPCVersion, ID: 1, Result: json.RawMessage(`{"ok":true}`)}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		URL:               server.URL,
		Provider:          "test",
		APIKey:            "partner",
		APISecret:         "secret",
		AllowedMethods:    []string{"swap_voucher_list"},
		RequestsPerMinute: 1,
		RateWindow:        time.Minute,
		Now:               func() time.Time { return now },
		Nonce: func() (string, error) {
			counter++
			return fmt.Sprintf("nonce-%d", counter), nil
		},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if err := client.call(context.Background(), "swap_voucher_list", []interface{}{int64(0), int64(0)}, nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := client.call(context.Background(), "swap_voucher_list", []interface{}{int64(0), int64(0)}, nil); err == nil {
		t.Fatalf("expected rate limit error on second call")
	}
}

// Ensure the custom client can decode JSON-RPC responses when a result is expected.
func TestClientDecodesResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		resp := rpcResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Result: json.RawMessage(`{"ok":true}`)}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		URL:               server.URL,
		Provider:          "test",
		APIKey:            "partner",
		APISecret:         "secret",
		AllowedMethods:    []string{"swap_voucher_list"},
		RequestsPerMinute: 10,
		Now:               func() time.Time { return time.Unix(1700000000, 0).UTC() },
		Nonce: func() (string, error) {
			return "nonce", nil
		},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var out map[string]interface{}
	if err := client.call(context.Background(), "swap_voucher_list", []interface{}{int64(0), int64(0)}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("expected result to populate output")
	}
}
