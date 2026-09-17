package auth

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLevelDBNoncePersistenceAuthenticatorRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonces")
	backend, err := NewLevelDBNoncePersistence(path)
	if err != nil {
		t.Fatalf("open persistence: %v", err)
	}
	var initial *LevelDBNoncePersistence = backend
	t.Cleanup(func() {
		if initial != nil {
			_ = initial.Close()
		}
	})
	now := time.Unix(1_717_787_717, 0).UTC()
	payload := []byte("payload")
	timestamp := now.Unix()

	makeRequest := func(ts int64, nonce string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "https://example.test/v1/resource", nil)
		req.Header.Set(HeaderAPIKey, "partner")
		tsHeader := strconv.FormatInt(ts, 10)
		req.Header.Set(HeaderTimestamp, tsHeader)
		req.Header.Set(HeaderNonce, nonce)
		sig := ComputeSignature("secret", tsHeader, nonce, http.MethodPost, CanonicalRequestPath(req), payload)
		req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
		return req
	}

	auth := NewAuthenticator(map[string]string{"partner": "secret"}, time.Minute, 5*time.Minute, 32, func() time.Time { return now }, backend)
	cutoff := now.Add(-5 * time.Minute)
	if err := auth.HydrateNonces(context.Background(), cutoff); err != nil {
		t.Fatalf("hydrate nonces: %v", err)
	}

	nonce := "nonce-restart"
	req := makeRequest(timestamp, nonce)
	if _, err := auth.Authenticate(req, payload); err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	if err := backend.Close(); err != nil {
		t.Fatalf("close persistence: %v", err)
	}
	initial = nil

	reopened, err := NewLevelDBNoncePersistence(path)
	if err != nil {
		t.Fatalf("reopen persistence: %v", err)
	}
	defer reopened.Close()

	authRestart := NewAuthenticator(map[string]string{"partner": "secret"}, time.Minute, 5*time.Minute, 32, func() time.Time { return now }, reopened)
	if err := authRestart.HydrateNonces(context.Background(), cutoff); err != nil {
		t.Fatalf("hydrate restart: %v", err)
	}
	if _, err := authRestart.Authenticate(makeRequest(timestamp, nonce), payload); err == nil || err.Error() != "nonce already used" {
		t.Fatalf("expected nonce replay after restart, got %v", err)
	}

	authCold := NewAuthenticator(map[string]string{"partner": "secret"}, time.Minute, 5*time.Minute, 32, func() time.Time { return now }, reopened)
	if _, err := authCold.Authenticate(makeRequest(timestamp, nonce), payload); err == nil || err.Error() != "nonce already used" {
		t.Fatalf("expected persistence to reject nonce, got %v", err)
	}
}
