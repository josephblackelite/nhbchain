package netsec

import (
	"fmt"
	"testing"
	"time"

	"nhbchain/p2p"
)

func TestNonceGuardBoundedUnderFlood(t *testing.T) {
	guard := p2p.NewNonceReplayGuard(time.Minute)
	if closable, ok := guard.(interface{ Close() }); ok {
		t.Cleanup(closable.Close)
	}

	const limit = 512
	if adjustable, ok := guard.(interface{ SetMaxEntries(int) }); ok {
		adjustable.SetMaxEntries(limit)
	} else {
		t.Fatalf("nonce guard does not expose SetMaxEntries for testing")
	}

	nodeID := "0x1234567890abcdef1234567890abcdef12345678"
	now := time.Now()
	for i := 0; i < limit*4; i++ {
		nonce := fmt.Sprintf("0x%08x", i)
		if !guard.Remember(nodeID, nonce, now.Add(time.Duration(i))) {
			t.Fatalf("expected nonce %d to be accepted", i)
		}
	}

	sized, ok := guard.(interface{ Size() int })
	if !ok {
		t.Fatalf("nonce guard does not expose Size for testing")
	}
	if size := sized.Size(); size > limit {
		t.Fatalf("expected guard size to stay <= %d, got %d", limit, size)
	}

	if !guard.Remember(nodeID, "0x00000000", now.Add(time.Hour)) {
		t.Fatalf("expected oldest nonce to be accepted after eviction")
	}
}

func TestNonceGuardRejectsExpiredReuse(t *testing.T) {
	guard := p2p.NewNonceReplayGuard(10 * time.Millisecond)
	if closable, ok := guard.(interface{ Close() }); ok {
		t.Cleanup(closable.Close)
	}

	nodeID := "0xabcdef0123456789abcdef0123456789abcdef01"
	nonce := "0xfeedc0de"

	start := time.Now()
	if !guard.Remember(nodeID, nonce, start) {
		t.Fatalf("expected nonce to be accepted initially")
	}

	time.Sleep(20 * time.Millisecond)
	if guard.Remember(nodeID, nonce, time.Now()) {
		t.Fatalf("expected replay to be rejected even after ttl expiration")
	}
}
