package p2p

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func peerstoreEntryAt(i int, seen time.Time) PeerstoreEntry {
	return PeerstoreEntry{
		Addr:     fmt.Sprintf("10.20.%d.%d:26656", (i>>8)&0xff, i&0xff),
		NodeID:   fmt.Sprintf("node-%05d", i),
		LastSeen: seen,
	}
}

func countPersistedPeers(t *testing.T, store *Peerstore) int {
	t.Helper()
	iter := store.db.NewIterator(nil, nil)
	defer iter.Release()
	count := 0
	for iter.Next() {
		if strings.HasPrefix(string(iter.Key()), "peer:") {
			count++
		}
	}
	if err := iter.Error(); err != nil {
		t.Fatalf("iterate peerstore: %v", err)
	}
	return count
}

func TestPeerstoreStaysWithinItsDefaultCap(t *testing.T) {
	store := newTestPeerstore(t)
	now := time.Now()
	for i := 0; i < defaultPeerstoreMaxEntries+1500; i++ {
		if err := store.Put(peerstoreEntryAt(i, now.Add(time.Duration(i)*time.Millisecond))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if got := len(store.Snapshot()); got > defaultPeerstoreMaxEntries {
		t.Fatalf("peerstore holds %d entries, cap is %d", got, defaultPeerstoreMaxEntries)
	}
	if got := countPersistedPeers(t, store); got > defaultPeerstoreMaxEntries {
		t.Fatalf("peerstore persisted %d records, cap is %d", got, defaultPeerstoreMaxEntries)
	}
}

func TestPeerstoreEvictsTheLeastRecentlySeenFirst(t *testing.T) {
	store := newTestPeerstore(t)
	store.SetLimits(3, 0)
	base := time.Now()
	for i := 0; i < 3; i++ {
		if err := store.Put(peerstoreEntryAt(i, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if err := store.Put(peerstoreEntryAt(3, base.Add(3*time.Second))); err != nil {
		t.Fatalf("put 3: %v", err)
	}
	if _, ok := store.ByNodeID("node-00000"); ok {
		t.Fatalf("expected the least recently seen peer to be evicted")
	}
	if got := store.Len(); got != 3 {
		t.Fatalf("expected 3 entries, got %d", got)
	}

	// Being seen again protects an entry from the next eviction.
	if _, err := store.RecordSuccess("node-00001", base.Add(10*time.Second)); err != nil {
		t.Fatalf("record success: %v", err)
	}
	if err := store.Put(peerstoreEntryAt(4, base.Add(11*time.Second))); err != nil {
		t.Fatalf("put 4: %v", err)
	}
	if _, ok := store.ByNodeID("node-00001"); !ok {
		t.Fatalf("expected the recently seen peer to be kept")
	}
	if _, ok := store.ByNodeID("node-00002"); ok {
		t.Fatalf("expected the next least recently seen peer to be evicted")
	}
	if got := countPersistedPeers(t, store); got != 3 {
		t.Fatalf("expected evicted peers to be deleted from disk too, %d records persisted", got)
	}
}

func TestPeerstoreEvictionSparesProtectedAndBannedPeers(t *testing.T) {
	store := newTestPeerstore(t)
	store.SetLimits(3, 0)
	base := time.Now()
	for i := 0; i < 3; i++ {
		if err := store.Put(peerstoreEntryAt(i, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	store.Protect("node-00000")
	if err := store.SetBan("node-00001", base.Add(time.Hour)); err != nil {
		t.Fatalf("ban: %v", err)
	}

	for i := 3; i < 10; i++ {
		if err := store.Put(peerstoreEntryAt(i, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if _, ok := store.ByNodeID("node-00000"); !ok {
		t.Fatalf("expected the protected peer to survive eviction")
	}
	if entry, ok := store.ByNodeID("node-00001"); !ok || !entry.BannedUntil.After(base) {
		t.Fatalf("expected the banned peer to survive eviction, got %+v ok=%v", entry, ok)
	}
	if got := store.Len(); got != 3 {
		t.Fatalf("expected 3 entries, got %d", got)
	}

	store.Unprotect("node-00000")
	if err := store.Put(peerstoreEntryAt(10, base.Add(20*time.Second))); err != nil {
		t.Fatalf("put 10: %v", err)
	}
	if _, ok := store.ByNodeID("node-00000"); ok {
		t.Fatalf("expected the peer to be evictable again once unprotected")
	}
}

func TestPeerstoreFullOfProtectedPeersRejectsNewOnes(t *testing.T) {
	store := newTestPeerstore(t)
	store.SetLimits(2, 0)
	now := time.Now()
	for i := 0; i < 2; i++ {
		if err := store.Put(peerstoreEntryAt(i, now)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		store.Protect(fmt.Sprintf("node-%05d", i))
	}
	if err := store.Put(peerstoreEntryAt(2, now)); err == nil {
		t.Fatalf("expected a new peer to be refused while every entry is protected")
	}
	if got := store.Len(); got != 2 {
		t.Fatalf("expected 2 entries, got %d", got)
	}
}

func TestPeerstorePruneDropsPeersNotSeenForTheTTL(t *testing.T) {
	store := newTestPeerstore(t)
	now := time.Now()
	store.SetLimits(0, 24*time.Hour)
	old := now.Add(-48 * time.Hour)
	for i, seen := range []time.Time{old, old, old, now} {
		if err := store.Put(peerstoreEntryAt(i, seen)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	store.Protect("node-00001")
	if err := store.SetBan("node-00002", now.Add(time.Hour)); err != nil {
		t.Fatalf("ban: %v", err)
	}

	if removed := store.Prune(now); removed != 1 {
		t.Fatalf("expected 1 peer to be pruned, got %d", removed)
	}
	if _, ok := store.ByNodeID("node-00000"); ok {
		t.Fatalf("expected the expired peer to be pruned")
	}
	for _, id := range []string{"node-00001", "node-00002", "node-00003"} {
		if _, ok := store.ByNodeID(id); !ok {
			t.Fatalf("expected %s to be kept", id)
		}
	}
	if got := countPersistedPeers(t, store); got != 3 {
		t.Fatalf("expected the pruned peer to be deleted from disk, %d records persisted", got)
	}
}

func TestPeerstoreRefusesOversizedRecords(t *testing.T) {
	store := newTestPeerstore(t)
	if err := store.Put(PeerstoreEntry{NodeID: "node-a", Addr: strings.Repeat("a", 1<<16)}); err == nil {
		t.Fatalf("expected an oversized address to be refused")
	}
	if err := store.Put(PeerstoreEntry{NodeID: strings.Repeat("n", 1<<16), Addr: "10.0.0.1:1"}); err == nil {
		t.Fatalf("expected an oversized node ID to be refused")
	}
	if got := len(store.Snapshot()); got != 0 {
		t.Fatalf("expected nothing to be stored, got %d entries", got)
	}
}

func TestPeerstoreLoadKeepsOnlyTheMostRecentlySeenRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.db")
	store, err := NewPeerstore(path, time.Second, time.Minute)
	if err != nil {
		t.Fatalf("new peerstore: %v", err)
	}
	// A database that an earlier version filled without limit.
	now := time.Now()
	total := defaultPeerstoreMaxEntries + 3000
	for i := 0; i < total; i++ {
		rec := peerstoreEntryAt(i, now.Add(-time.Duration(i)*time.Second))
		blob, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal %d: %v", i, err)
		}
		if err := store.db.Put([]byte("peer:"+rec.NodeID), blob, nil); err != nil {
			t.Fatalf("seed record %d: %v", i, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := NewPeerstore(path, time.Second, time.Minute)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if got := len(reopened.Snapshot()); got != defaultPeerstoreMaxEntries {
		t.Fatalf("expected load to keep %d entries, got %d", defaultPeerstoreMaxEntries, got)
	}
	if _, ok := reopened.ByNodeID(peerstoreEntryAt(0, now).NodeID); !ok {
		t.Fatalf("expected the most recently seen peer to be kept")
	}
	if _, ok := reopened.ByNodeID(peerstoreEntryAt(total-1, now).NodeID); ok {
		t.Fatalf("expected the least recently seen peer to be dropped")
	}
	if got := countPersistedPeers(t, reopened); got != defaultPeerstoreMaxEntries {
		t.Fatalf("expected the dropped records to be deleted from disk, %d records persisted", got)
	}
}

func TestPeerstoreLoadSkipsDamagedOversizedAndExpiredRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.db")
	store, err := NewPeerstore(path, time.Second, time.Minute)
	if err != nil {
		t.Fatalf("new peerstore: %v", err)
	}
	now := time.Now()
	good := peerstoreEntryAt(1, now)
	blob, _ := json.Marshal(good)
	huge, _ := json.Marshal(PeerstoreEntry{NodeID: "node-huge", Addr: strings.Repeat("h", 1<<20), LastSeen: now})
	stale, _ := json.Marshal(peerstoreEntryAt(2, now.Add(-100*24*time.Hour)))
	bannedStale := peerstoreEntryAt(3, now.Add(-100*24*time.Hour))
	bannedStale.BannedUntil = now.Add(time.Hour)
	bannedBlob, _ := json.Marshal(bannedStale)
	mismatched, _ := json.Marshal(peerstoreEntryAt(4, now))
	for key, value := range map[string][]byte{
		"peer:" + good.NodeID:               blob,
		"peer:node-damaged":                 []byte("{not json"),
		"peer:node-huge":                    huge,
		"peer:" + "node-00002":              stale,
		"peer:" + "node-00003":              bannedBlob,
		"peer:some-other-node-id":           mismatched,
		"unrelated:key":                     []byte("kept"),
		"peer:" + strings.Repeat("n", 1000): blob,
	} {
		if err := store.db.Put([]byte(key), value, nil); err != nil {
			t.Fatalf("seed %q: %v", key, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := NewPeerstore(path, time.Second, time.Minute)
	if err != nil {
		t.Fatalf("a damaged record must not keep the peerstore from opening: %v", err)
	}
	defer reopened.Close()
	got := map[string]bool{}
	for _, entry := range reopened.Snapshot() {
		got[entry.NodeID] = true
	}
	if !got[good.NodeID] || !got["node-00003"] || len(got) != 2 {
		t.Fatalf("expected only the good and the still-banned records to be loaded, got %v", got)
	}
	if count := countPersistedPeers(t, reopened); count != 2 {
		t.Fatalf("expected the unusable records to be deleted from disk, %d records persisted", count)
	}
	if value, err := reopened.db.Get([]byte("unrelated:key"), nil); err != nil || !bytes.Equal(value, []byte("kept")) {
		t.Fatalf("records that are not peers must be left alone: %q %v", value, err)
	}
}

func TestHandshakeFromAForgedIdentityDoesNotFillThePeerstore(t *testing.T) {
	genesis := bytes.Repeat([]byte{0xAA}, 32)
	local := NewServer(noopHandler{}, mustKey(t), baseConfig(genesis))
	store := newTestPeerstore(t)
	local.SetPeerstore(store)
	remote := NewServer(noopHandler{}, mustKey(t), baseConfig(genesis))

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	const attempts = defaultReputationMaxRecords + 500
	for i := 0; i < attempts; i++ {
		forged := *packet
		forged.NodeID = fmt.Sprintf("0x%064x", i+1)
		if err := local.verifyHandshake(&forged); err == nil {
			t.Fatalf("attempt %d: expected a forged identity to be refused", i)
		}
	}
	if got := len(store.Snapshot()); got != 0 {
		t.Fatalf("identities nobody proved must not be written to the peerstore, got %d entries", got)
	}
	local.reputation.mu.Lock()
	tracked := len(local.reputation.records)
	local.reputation.mu.Unlock()
	if tracked > defaultReputationMaxRecords {
		t.Fatalf("reputation table holds %d peers, cap is %d", tracked, defaultReputationMaxRecords)
	}
}
