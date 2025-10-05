package auth

import (
	"fmt"
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
