package p2p

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestPeerstore(t *testing.T) *Peerstore {
	t.Helper()
	dir := t.TempDir()
	store, err := NewPeerstore(filepath.Join(dir, "peers.db"), 500*time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatalf("new peerstore: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func TestPeerstoreBanExpiry(t *testing.T) {
	store := newTestPeerstore(t)
	rec := PeerstoreEntry{Addr: "127.0.0.1:1000", NodeID: "node-ban"}
	if err := store.Put(rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	now := time.Unix(0, 0)
	until := now.Add(2 * time.Minute)
	if err := store.SetBan("node-ban", until); err != nil {
		t.Fatalf("set ban: %v", err)
	}
	if !store.IsBanned("node-ban", now.Add(time.Minute)) {
		t.Fatalf("expected peer to be banned before expiry")
	}
	if store.IsBanned("node-ban", until.Add(time.Second)) {
		t.Fatalf("expected ban to expire")
	}
}

func TestPeerstoreBackoffGrowthAndReset(t *testing.T) {
	store := newTestPeerstore(t)
	rec := PeerstoreEntry{Addr: "127.0.0.1:2000", NodeID: "node-backoff"}
	if err := store.Put(rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	base := 500 * time.Millisecond
	now := time.Unix(0, 0)
	for i := 0; i < 4; i++ {
		next, err := store.RecordFail("node-backoff", now)
		if err != nil {
			t.Fatalf("fail %d: %v", i, err)
		}
		expectedDelay := base * time.Duration(1<<uint(i))
		if expectedDelay > 5*time.Second {
			expectedDelay = 5 * time.Second
		}
		want := now.Add(expectedDelay)
		dial := store.NextDialAt("127.0.0.1:2000", now)
		if dial != want {
			t.Fatalf("fail %d: expected dial at %v got %v", i, want, dial)
		}
		now = now.Add(10 * time.Millisecond)
		_ = next
	}
	now = now.Add(time.Second)
	if _, err := store.RecordSuccess("node-backoff", now); err != nil {
		t.Fatalf("success: %v", err)
	}
	dial := store.NextDialAt("127.0.0.1:2000", now)
	if !dial.Equal(now) {
		t.Fatalf("expected backoff reset to now got %v", dial)
	}
}

func TestPeerstoreScoreIncrementAndDecay(t *testing.T) {
	store := newTestPeerstore(t)
	rec := PeerstoreEntry{Addr: "127.0.0.1:3000", NodeID: "node-score"}
	if err := store.Put(rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	now := time.Unix(0, 0)
	if updated, err := store.RecordSuccess("node-score", now); err != nil {
		t.Fatalf("success: %v", err)
	} else if updated.Score != 1 {
		t.Fatalf("expected score 1 got %v", updated.Score)
	}
	now = now.Add(time.Second)
	if updated, err := store.RecordFail("node-score", now); err != nil {
		t.Fatalf("fail: %v", err)
	} else if updated.Score >= 1 {
		t.Fatalf("expected score to decay, got %v", updated.Score)
	}
}

func TestPeerstoreDedupeByNodeID(t *testing.T) {
	store := newTestPeerstore(t)
	rec := PeerstoreEntry{Addr: "127.0.0.1:4000", NodeID: "node-dedupe"}
	if err := store.Put(rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	rec.Addr = "127.0.0.1:4001"
	if err := store.Put(rec); err != nil {
		t.Fatalf("put2: %v", err)
	}
	if _, ok := store.Get("127.0.0.1:4000"); ok {
		t.Fatalf("old address should not exist")
	}
	if got, ok := store.Get("127.0.0.1:4001"); !ok {
		t.Fatalf("expected updated address")
	} else if got.NodeID != "node-dedupe" {
		t.Fatalf("expected node ID preserved, got %s", got.NodeID)
	}
}
