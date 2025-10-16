package auth

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestNonceStoreCapacityEviction(t *testing.T) {
	store := newNonceStore(5*time.Minute, 3)
	base := time.Unix(1700000000, 0).UTC()

	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("nonce-%d", i)
		if seen := store.Seen(key, base); seen {
			t.Fatalf("expected first observation of %s to be false", key)
		}
	}
	if got := len(store.entries); got != 3 {
		t.Fatalf("expected 3 entries after initial fill, got %d", got)
	}

	if seen := store.Seen("nonce-3", base); seen {
		t.Fatalf("expected new key to be accepted after capacity eviction")
	}
	if got := len(store.entries); got != 3 {
		t.Fatalf("expected capacity to remain at 3, got %d", got)
	}
	if _, exists := store.entries["nonce-0"]; exists {
		t.Fatalf("expected oldest nonce to be evicted when capacity exceeded")
	}
	if seen := store.Seen("nonce-1", base); !seen {
		t.Fatalf("expected recently seen nonce to be reported as duplicate")
	}

	if seen := store.Seen("nonce-4", base); seen {
		t.Fatalf("expected new key to be accepted after second eviction")
	}
	if got := len(store.entries); got != 3 {
		t.Fatalf("expected capacity to remain bounded at 3, got %d", got)
	}
}

func TestNewAuthenticatorClampsSecurityParameters(t *testing.T) {
	auth := NewAuthenticator(map[string]string{"a": "secret"}, 15*time.Minute, 30*time.Minute, 1_000_000, time.Now, nil)
	if auth.allowedTimestampSkew != maxAllowedTimestampSkew {
		t.Fatalf("expected timestamp skew to clamp to %s, got %s", maxAllowedTimestampSkew, auth.allowedTimestampSkew)
	}
	if auth.nonceTTL != maxNonceWindow {
		t.Fatalf("expected nonce TTL to clamp to %s, got %s", maxNonceWindow, auth.nonceTTL)
	}
	if auth.nonceCapacity != maxNonceCapacity {
		t.Fatalf("expected nonce capacity to clamp to %d, got %d", maxNonceCapacity, auth.nonceCapacity)
	}
}

func TestNonceStoreExpiresOldEntries(t *testing.T) {
	store := newNonceStore(30*time.Second, 5)
	base := time.Unix(1700000000, 0).UTC()

	if store.Seen("nonce-a", base) {
		t.Fatalf("expected first nonce to be new")
	}
	if store.Seen("nonce-b", base.Add(5*time.Second)) {
		t.Fatalf("expected second nonce to be new")
	}

	future := base.Add(1 * time.Minute)
	if store.Seen("nonce-c", future) {
		t.Fatalf("expected new nonce to be accepted after expiration window")
	}
	if _, exists := store.entries["nonce-a"]; exists {
		t.Fatalf("expected expired nonce-a to be pruned")
	}
	if _, exists := store.entries["nonce-b"]; exists {
		t.Fatalf("expected expired nonce-b to be pruned")
	}

	if store.Seen("nonce-b", future) {
		t.Fatalf("expected nonce-b to be treated as new after expiration")
	}
}

func TestAuthenticatorPersistsNonceUsage(t *testing.T) {
	backend := newFakePersistence()
	now := time.Unix(1_700_000_000, 0).UTC()
	payload := []byte("payload")
	makeRequest := func(ts, nonce string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "https://example.test/v1/resource", nil)
		req.Header.Set(HeaderAPIKey, "partner")
		req.Header.Set(HeaderTimestamp, ts)
		req.Header.Set(HeaderNonce, nonce)
		sig := ComputeSignature("secret", ts, nonce, http.MethodPost, CanonicalRequestPath(req), payload)
		req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
		return req
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	nonce := "nonce-42"
	auth := NewAuthenticator(map[string]string{"partner": "secret"}, 2*time.Minute, 5*time.Minute, 16, func() time.Time { return now }, backend)
	cutoff := now.Add(-5 * time.Minute)
	if err := auth.HydrateNonces(context.Background(), cutoff); err != nil {
		t.Fatalf("hydrate nonces: %v", err)
	}
	req := makeRequest(timestamp, nonce)
	principal, err := auth.Authenticate(req, payload)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if principal.APIKey != "partner" {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	if count := backend.Count(); count != 1 {
		t.Fatalf("unexpected persisted nonce count: %d", count)
	}

	authRestart := NewAuthenticator(map[string]string{"partner": "secret"}, 2*time.Minute, 5*time.Minute, 16, func() time.Time { return now }, backend)
	if err := authRestart.HydrateNonces(context.Background(), cutoff); err != nil {
		t.Fatalf("hydrate restart: %v", err)
	}
	if _, err := authRestart.Authenticate(makeRequest(timestamp, nonce), payload); err == nil || err.Error() != "nonce already used" {
		t.Fatalf("expected nonce replay after hydration, got %v", err)
	}

	authCold := NewAuthenticator(map[string]string{"partner": "secret"}, 2*time.Minute, 5*time.Minute, 16, func() time.Time { return now }, backend)
	if _, err := authCold.Authenticate(makeRequest(timestamp, nonce), payload); err == nil || err.Error() != "nonce already used" {
		t.Fatalf("expected nonce replay via persistence, got %v", err)
	}
}

type fakePersistence struct {
	mu      sync.Mutex
	records map[string]NonceRecord
}

func newFakePersistence() *fakePersistence {
	return &fakePersistence{records: make(map[string]NonceRecord)}
}

func (f *fakePersistence) EnsureNonce(ctx context.Context, record NonceRecord) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.records == nil {
		f.records = make(map[string]NonceRecord)
	}
	key := record.APIKey + "|" + record.Timestamp + "|" + record.Nonce
	if existing, ok := f.records[key]; ok {
		if record.ObservedAt.After(existing.ObservedAt) {
			f.records[key] = record
		}
		return true, nil
	}
	f.records[key] = record
	return false, nil
}

func (f *fakePersistence) RecentNonces(ctx context.Context, cutoff time.Time) ([]NonceRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]NonceRecord, 0, len(f.records))
	for _, rec := range f.records {
		if rec.ObservedAt.Before(cutoff) {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

func (f *fakePersistence) PruneNonces(ctx context.Context, cutoff time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, rec := range f.records {
		if rec.ObservedAt.Before(cutoff) {
			delete(f.records, key)
		}
	}
	return nil
}

func (f *fakePersistence) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}
