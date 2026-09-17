package identitygateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

type captureEmailer struct {
	mu      sync.Mutex
	lastMsg VerificationMessage
	sent    int
}

func (c *captureEmailer) SendVerification(_ context.Context, msg VerificationMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent++
	c.lastMsg = msg
	return nil
}

func TestEmailVerificationFlow(t *testing.T) {
	t.Parallel()

	store, err := NewStore(filepath.Join(t.TempDir(), "identity.db"), &bolt.Options{Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	emailer := &captureEmailer{}
	cfg := Config{
		APIKeys:          map[string]string{"test": "secret"},
		EmailSalt:        []byte("super-secret-salt"),
		CodeTTL:          time.Minute,
		RegisterWindow:   time.Hour,
		RegisterAttempts: 5,
		TimestampSkew:    time.Hour,
		IdempotencyTTL:   time.Hour,
	}
	server, err := NewServer(store, emailer, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	var current time.Time
	current = now
	server.nowFn = func() time.Time { return current }
	server.codeFn = func() (string, error) { return "483921", nil }

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	// Register email and ensure code dispatched.
	respBody := doRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/register", map[string]any{
		"email":     "Frank.Example@Example.com",
		"aliasHint": "frankrocks",
	}, "test", "secret", current, "register-key")
	if emailer.sent != 1 {
		t.Fatalf("expected verification email to be dispatched")
	}
	var registerResp struct {
		Status    string `json:"status"`
		ExpiresIn int    `json:"expiresIn"`
	}
	if err := json.Unmarshal(respBody, &registerResp); err != nil {
		t.Fatalf("unmarshal register: %v", err)
	}
	if registerResp.Status != "pending" {
		t.Fatalf("unexpected status %q", registerResp.Status)
	}
	if registerResp.ExpiresIn != 60 {
		t.Fatalf("expected expiresIn=60, got %d", registerResp.ExpiresIn)
	}

	// Replay with idempotency should return cached response.
	current = current.Add(5 * time.Second)
	resp := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/register", map[string]any{
		"email": "Frank.Example@Example.com",
	}, "test", "secret", current, "register-key")
	if got := resp.Header.Get("X-Idempotency-Cache"); got != "hit" {
		t.Fatalf("expected idempotency cache hit, got %q", got)
	}

	// Complete verification.
	current = current.Add(10 * time.Second)
	verifyBody := doRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/verify", map[string]any{
		"email": "frank.example@example.com",
		"code":  emailer.lastMsg.Code,
	}, "test", "secret", current, "")
	var verifyResp struct {
		Status     string `json:"status"`
		EmailHash  string `json:"emailHash"`
		VerifiedAt string `json:"verifiedAt"`
	}
	if err := json.Unmarshal(verifyBody, &verifyResp); err != nil {
		t.Fatalf("unmarshal verify: %v", err)
	}
	if verifyResp.Status != "verified" {
		t.Fatalf("unexpected verify status %q", verifyResp.Status)
	}
	expectedHash := computeEmailHash("frank.example@example.com", cfg.EmailSalt)
	if verifyResp.EmailHash != expectedHash {
		t.Fatalf("expected email hash %s, got %s", expectedHash, verifyResp.EmailHash)
	}

	// Bind alias to verified email.
	current = current.Add(15 * time.Second)
	bindBody := doRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/alias/bind-email", map[string]any{
		"aliasId": "0x1234abcd",
		"email":   "frank.example@example.com",
		"consent": true,
	}, "test", "secret", current, "")
	var bindResp struct {
		Status       string `json:"status"`
		AliasID      string `json:"aliasId"`
		EmailHash    string `json:"emailHash"`
		PublicLookup bool   `json:"publicLookup"`
	}
	if err := json.Unmarshal(bindBody, &bindResp); err != nil {
		t.Fatalf("unmarshal bind: %v", err)
	}
	if bindResp.Status != "linked" || !bindResp.PublicLookup {
		t.Fatalf("unexpected bind response: %+v", bindResp)
	}
	if bindResp.EmailHash != expectedHash {
		t.Fatalf("email hash mismatch: %s != %s", bindResp.EmailHash, expectedHash)
	}
}

func TestRegisterRateLimit(t *testing.T) {
	t.Parallel()

	store, err := NewStore(filepath.Join(t.TempDir(), "identity.db"), &bolt.Options{Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	emailer := &captureEmailer{}
	cfg := Config{
		APIKeys:          map[string]string{"test": "secret"},
		EmailSalt:        []byte("salt"),
		CodeTTL:          time.Minute,
		RegisterWindow:   time.Hour,
		RegisterAttempts: 5,
		TimestampSkew:    time.Hour,
		IdempotencyTTL:   time.Hour,
	}
	server, err := NewServer(store, emailer, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	current := now
	server.nowFn = func() time.Time { return current }
	server.codeFn = func() (string, error) { return "123456", nil }

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	for i := 0; i < 5; i++ {
		current = now.Add(time.Duration(i) * time.Minute)
		resp := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/register", map[string]any{
			"email": "rate@example.com",
		}, "test", "secret", current, "rate"+strconv.Itoa(i))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
	}

	current = now.Add(10 * time.Minute)
	resp := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/register", map[string]any{
		"email": "rate@example.com",
	}, "test", "secret", current, "rate-limit")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 response, got %d", resp.StatusCode)
	}
}

func doRequest(t *testing.T, client *http.Client, method, baseURL, endpoint string, body map[string]any, key, secret string, now time.Time, idem string) []byte {
	t.Helper()
	resp := doRawRequest(t, client, method, baseURL, endpoint, body, key, secret, now, idem)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode >= 400 {
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, string(payload))
	}
	return payload
}

func doRawRequest(t *testing.T, client *http.Client, method, baseURL, endpoint string, body map[string]any, key, secret string, now time.Time, idem string) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(method, baseURL+endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	signature := computeSignature([]byte(secret), method, endpoint, encoded, ts)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerAPIKey, key)
	req.Header.Set(headerAPISignature, signature)
	req.Header.Set(headerAPITimestamp, ts)
	if idem != "" {
		req.Header.Set(headerIdempotency, idem)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}
