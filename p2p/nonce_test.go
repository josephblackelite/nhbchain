package p2p

import (
	"testing"
	"time"
)

func TestNonceGuardRemembersPerNode(t *testing.T) {
	guard := newNonceGuard(5 * time.Millisecond)
	now := time.Now()

	if !guard.Remember("nodeA", "0xdeadbeef", now) {
		t.Fatalf("expected first nonce to be accepted")
	}

	later := now.Add(10 * time.Millisecond)
	if guard.Remember("nodeA", "0xdeadbeef", later) {
		t.Fatalf("expected replay for same node to be rejected")
	}

	if !guard.Remember("nodeB", "0xdeadbeef", later) {
		t.Fatalf("expected nonce reuse by different node to be accepted")
	}
}
