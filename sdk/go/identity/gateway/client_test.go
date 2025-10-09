package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRegisterEmailSignsRequest(t *testing.T) {
	t.Parallel()

	var captured struct {
		method string
		path   string
		body   string
		key    string
		sig    string
		ts     string
		idem   string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.body = string(body)
		captured.key = r.Header.Get("X-API-Key")
		captured.sig = r.Header.Get("X-API-Signature")
		captured.ts = r.Header.Get("X-API-Timestamp")
		captured.idem = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"pending","expiresIn":600}`))
	}))
	defer server.Close()

	fixed := time.Unix(1_700_000_000, 0).UTC()
	client, err := New(server.URL, "demo", "secret", WithHTTPClient(server.Client()), WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	resp, err := client.RegisterEmail(context.Background(), "User@example.com", "alias", WithIdempotencyKey("test-key"))
	if err != nil {
		t.Fatalf("register email: %v", err)
	}
	if resp.Status != "pending" || resp.ExpiresIn != 600 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if captured.method != http.MethodPost {
		t.Fatalf("expected POST, got %s", captured.method)
	}
	if captured.path != "/identity/email/register" {
		t.Fatalf("unexpected path %s", captured.path)
	}
	if captured.key != "demo" {
		t.Fatalf("unexpected api key %s", captured.key)
	}
	if captured.idem != "test-key" {
		t.Fatalf("expected idempotency header to propagate")
	}
	expectedPayload, _ := json.Marshal(map[string]string{
		"email":     "User@example.com",
		"aliasHint": "alias",
	})
	if captured.body != string(expectedPayload) {
		t.Fatalf("unexpected payload %s", captured.body)
	}
	expectedSig := client.sign(http.MethodPost, "/identity/email/register", expectedPayload, strconv.FormatInt(fixed.Unix(), 10))
	if captured.sig != expectedSig {
		t.Fatalf("signature mismatch: got %s want %s", captured.sig, expectedSig)
	}
	if captured.ts != strconv.FormatInt(fixed.Unix(), 10) {
		t.Fatalf("unexpected timestamp %s", captured.ts)
	}
}

func TestVerifyEmailPropagatesErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"IDN-401"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	client, err := New(server.URL, "demo", "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.VerifyEmail(context.Background(), "user@example.com", "123456")
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "identity gateway 401") {
		t.Fatalf("expected status in error, got %s", got)
	}
}
