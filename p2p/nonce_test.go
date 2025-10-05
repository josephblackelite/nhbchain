package p2p

import (
	"fmt"
	"testing"
	"time"
)

func TestNonceGuardRemembersPerNode(t *testing.T) {
	guard := newNonceGuard(5 * time.Millisecond)
	now := time.Now()

	if !guard.Remember("nodeA", "0xdeadbeef", now) {
		t.Fatalf("expected first nonce to be accepted")
	}

	withinWindow := now.Add(2 * time.Millisecond)
	if guard.Remember("nodeA", "0xdeadbeef", withinWindow) {
		t.Fatalf("expected replay within window for same node to be rejected")
	}

	later := now.Add(10 * time.Millisecond)
	if !guard.Remember("nodeA", "0xdeadbeef", later) {
		t.Fatalf("expected nonce to be accepted again after window expiry")
	}

	if !guard.Remember("nodeB", "0xdeadbeef", later) {
		t.Fatalf("expected nonce reuse by different node to be accepted")
	}
}

func TestNonceGuardPruneRemovesSeen(t *testing.T) {
	guard := newNonceGuard(2 * time.Millisecond)
	now := time.Now()

	if !guard.Remember("nodeA", "0x1", now) {
		t.Fatalf("expected nonce to be accepted initially")
	}

	// Advance past the window to trigger pruning.
	later := now.Add(5 * time.Millisecond)
	if !guard.Remember("nodeA", "0x2", later) {
		t.Fatalf("expected new nonce after pruning to be accepted")
	}

	guard.mu.Lock()
	defer guard.mu.Unlock()
	if _, exists := guard.seen[guard.fingerprint("nodeA", "0x1")]; exists {
		t.Fatalf("expected expired nonce fingerprint to be pruned from seen map")
	}
}

func TestNonceGuardEvictsWhenOverCapacity(t *testing.T) {
	guard := newNonceGuard(time.Minute)
	guard.maxEntries = 3
	now := time.Now()

	for i := 0; i < 3; i++ {
		nonce := fmt.Sprintf("0x%02x", i)
		if !guard.Remember("nodeA", nonce, now.Add(time.Duration(i))) {
			t.Fatalf("expected nonce %d to be accepted", i)
		}
	}

	// Adding a fourth should evict the oldest entry.
	if !guard.Remember("nodeA", "0x03", now.Add(3)) {
		t.Fatalf("expected nonce 3 to be accepted")
	}

	guard.mu.Lock()
	if len(guard.seen) != guard.maxEntries {
		guard.mu.Unlock()
		t.Fatalf("expected seen map to be capped at %d, got %d", guard.maxEntries, len(guard.seen))
	}
	guard.mu.Unlock()

	// Oldest entry should have been evicted and allowed again.
	if !guard.Remember("nodeA", "0x00", now.Add(4)) {
		t.Fatalf("expected oldest nonce to be accepted after eviction")
	}
}

func TestNonceGuardBoundedWithManyNonces(t *testing.T) {
	guard := newNonceGuard(time.Minute)
	guard.maxEntries = 5
	now := time.Now()

	for i := 0; i < 1000; i++ {
		nonce := fmt.Sprintf("0x%03x", i)
		if !guard.Remember("nodeA", nonce, now.Add(time.Duration(i))) {
			t.Fatalf("expected nonce %d to be accepted", i)
		}

		guard.mu.Lock()
		if len(guard.seen) > guard.maxEntries {
			guard.mu.Unlock()
			t.Fatalf("expected seen map to remain bounded by %d, got %d", guard.maxEntries, len(guard.seen))
		}
		guard.mu.Unlock()
	}
}
