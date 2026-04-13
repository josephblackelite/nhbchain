package p2p

import (
	"fmt"
	"testing"
	"time"
)

func TestNonceGuardRejectsReplaysAcrossTTL(t *testing.T) {
	guard := newNonceGuard(5 * time.Millisecond)
	defer guard.Close()

	now := time.Now()
	if !guard.Remember("nodeA", "0xdeadbeef", now) {
		t.Fatalf("expected first nonce to be accepted")
	}

	withinWindow := now.Add(2 * time.Millisecond)
	if guard.Remember("nodeA", "0xdeadbeef", withinWindow) {
		t.Fatalf("expected replay within ttl for same node to be rejected")
	}

	later := now.Add(10 * time.Millisecond)
	if guard.Remember("nodeA", "0xdeadbeef", later) {
		t.Fatalf("expected replay after ttl to remain rejected until eviction")
	}

	if !guard.Remember("nodeB", "0xdeadbeef", later) {
		t.Fatalf("expected nonce reuse by different node to be accepted")
	}
}

func TestNonceGuardSweepRemovesExpired(t *testing.T) {
	guard := newNonceGuard(2 * time.Millisecond)
	defer guard.Close()

	now := time.Now()
	if !guard.Remember("nodeA", "0x1", now) {
		t.Fatalf("expected nonce to be accepted initially")
	}

	cutoff := now.Add(5 * time.Millisecond)
	guard.RunJanitorSweep(cutoff)

	if guard.Size() != 0 {
		t.Fatalf("expected guard to remove expired entries, size=%d", guard.Size())
	}

	if !guard.Remember("nodeA", "0x2", cutoff) {
		t.Fatalf("expected new nonce after sweep to be accepted")
	}
}

func TestNonceGuardEvictsWhenOverCapacity(t *testing.T) {
	guard := newNonceGuard(time.Minute)
	defer guard.Close()
	guard.SetMaxEntries(3)

	now := time.Now()
	for i := 0; i < 3; i++ {
		nonce := fmt.Sprintf("0x%02x", i)
		if !guard.Remember("nodeA", nonce, now.Add(time.Duration(i))) {
			t.Fatalf("expected nonce %d to be accepted", i)
		}
	}

	if !guard.Remember("nodeA", "0x03", now.Add(3)) {
		t.Fatalf("expected nonce 3 to be accepted")
	}

	if guard.Size() != 3 {
		t.Fatalf("expected guard size to remain capped at 3, got %d", guard.Size())
	}

	if !guard.Remember("nodeA", "0x00", now.Add(4)) {
		t.Fatalf("expected oldest nonce to be accepted after eviction")
	}
}

func TestNonceGuardBoundedWithManyNonces(t *testing.T) {
	guard := newNonceGuard(time.Minute)
	defer guard.Close()
	guard.SetMaxEntries(5)

	now := time.Now()
	for i := 0; i < 1000; i++ {
		nonce := fmt.Sprintf("0x%03x", i)
		if !guard.Remember("nodeA", nonce, now.Add(time.Duration(i))) {
			t.Fatalf("expected nonce %d to be accepted", i)
		}

		if size := guard.Size(); size > 5 {
			t.Fatalf("expected guard size to remain bounded by 5, got %d", size)
		}
	}
}
