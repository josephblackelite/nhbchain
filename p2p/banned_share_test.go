package p2p

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// Entries under a ban are kept longer than the rest when a table is full, so
// that a ban is not forgotten early. That preference must have a limit: peers
// that are not banned, and peers we are connected to, must keep most of each
// table however many bans there are. These tests are about that limit. Bans
// are recorded for identities that remote peers choose, so the number of
// banned entries is not something the node can assume to be small.

func countBannedPeerstoreEntries(store *Peerstore, now time.Time) int {
	banned := 0
	for _, entry := range store.Snapshot() {
		if entry.BannedUntil.After(now) {
			banned++
		}
	}
	return banned
}

func countBannedReputationRecords(m *ReputationManager, now time.Time) int {
	banned := 0
	for _, status := range m.Snapshot(now) {
		if status.Banned {
			banned++
		}
	}
	return banned
}

// Handshakes for another chain, each signed by a different new key that names
// itself, are all refused and all banned. They must not push out the peerstore
// entries and reputation scores of the peers that are not banned.
func TestManySignedWrongChainHandshakesLeaveTheTablesToTheOtherPeers(t *testing.T) {
	const (
		capacity = 128
		known    = 100
		quota    = capacity / 4
	)
	cfg := identityTestConfig(0xE1)
	local := NewServer(noopHandler{}, mustKey(t), cfg)
	base := time.Now()
	local.now = func() time.Time { return base }
	local.reputation = NewReputationManager(ReputationConfig{
		BanScore:    cfg.BanScore,
		GreyScore:   cfg.GreyScore,
		BanDuration: cfg.PeerBanDuration,
		MaxRecords:  capacity,
	})
	store := newTestPeerstore(t)
	store.SetLimits(capacity, 0)
	local.SetPeerstore(store)

	// Peers that were seen before, the oldest of them connected right now, and
	// one of them with a penalty on its reputation record.
	for i := 0; i < known; i++ {
		if err := store.Put(peerstoreEntryAt(i, base.Add(-time.Hour+time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	connected := peerstoreEntryAt(0, base).NodeID
	store.Protect(connected)
	const penalized = "0xpenalized-but-honest"
	local.reputation.PenalizeMalformed(penalized, base, false)
	wantScore := local.reputation.Score(penalized, base)
	if wantScore == 0 {
		t.Fatalf("the penalty did not register")
	}

	for i := 0; i < 2*capacity; i++ {
		key := mustKey(t)
		nodeID := normalizeHex(deriveNodeIDFromPub(&key.PrivateKey.PublicKey))
		packet := forgedHandshake(t, key, nodeID, 2, cfg.GenesisHash)
		if !handshakeSignedByClaimedNode(packet) {
			t.Fatalf("attempt %d: the packet should be signed by the node it names", i)
		}
		if err := local.verifyHandshake(packet); !errors.Is(err, errHandshakeChainMismatch) {
			t.Fatalf("attempt %d: expected a chain mismatch, got %v", i, err)
		}
	}

	if got := store.Len(); got > capacity {
		t.Fatalf("peerstore holds %d entries, cap is %d", got, capacity)
	}
	if got := countPersistedPeers(t, store); got > capacity {
		t.Fatalf("peerstore persisted %d records, cap is %d", got, capacity)
	}
	if _, ok := store.ByNodeID(connected); !ok {
		t.Fatalf("the connected peer must keep its peerstore entry")
	}
	// Bans may displace some of the other entries, but only a bounded share of
	// the table: a quarter, plus the entry that is being added.
	kept := 0
	for i := 0; i < known; i++ {
		if _, ok := store.ByNodeID(peerstoreEntryAt(i, base).NodeID); ok {
			kept++
		}
	}
	if lost := known - kept; lost > quota+1 {
		t.Fatalf("%d of %d earlier peerstore entries were displaced by bans, at most %d may be", lost, known, quota+1)
	}
	if banned := countBannedPeerstoreEntries(store, base); banned > quota+1 {
		t.Fatalf("%d banned peerstore entries, at most %d may be kept", banned, quota+1)
	}

	if got := local.reputation.Score(penalized, base); got != wantScore {
		t.Fatalf("the reputation record of a peer that is not banned was lost: score %d, want %d", got, wantScore)
	}
	local.reputation.mu.Lock()
	tracked := len(local.reputation.records)
	local.reputation.mu.Unlock()
	if tracked > capacity {
		t.Fatalf("reputation table holds %d peers, cap is %d", tracked, capacity)
	}
}

func TestPeerstoreKeepsBansUpToAQuarterOfItsCapacity(t *testing.T) {
	const capacity = 64
	store := newTestPeerstore(t)
	store.SetLimits(capacity, 0)
	base := time.Now()
	until := base.Add(time.Hour)

	// Within the quarter, every ban survives a long run of new peers.
	for i := 0; i < capacity/4-6; i++ {
		if err := store.Put(peerstoreEntryAt(i, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		if err := store.SetBan(peerstoreEntryAt(i, base).NodeID, until); err != nil {
			t.Fatalf("ban %d: %v", i, err)
		}
	}
	next := capacity/4 - 6
	for ; next < 10*capacity; next++ {
		if err := store.Put(peerstoreEntryAt(next, base.Add(time.Duration(next)*time.Second))); err != nil {
			t.Fatalf("put %d: %v", next, err)
		}
	}
	if got := countBannedPeerstoreEntries(store, base); got != capacity/4-6 {
		t.Fatalf("expected the %d bans within the quarter to survive, %d did", capacity/4-6, got)
	}

	// Beyond the quarter, the oldest bans go first and the quarter is kept.
	for i := next; i < next+40; i++ {
		if err := store.Put(peerstoreEntryAt(i, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		if err := store.SetBan(peerstoreEntryAt(i, base).NodeID, until); err != nil {
			t.Fatalf("ban %d: %v", i, err)
		}
	}
	last := next + 40
	for i := last; i < last+capacity; i++ {
		if err := store.Put(peerstoreEntryAt(i, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if got := countBannedPeerstoreEntries(store, base); got != capacity/4 {
		t.Fatalf("expected the bans to be cut down to %d, %d remain", capacity/4, got)
	}
	if _, ok := store.ByNodeID(peerstoreEntryAt(next, base).NodeID); ok {
		t.Fatalf("the oldest ban above the quarter should have been dropped first")
	}
	if entry, ok := store.ByNodeID(peerstoreEntryAt(last-1, base).NodeID); !ok || !entry.BannedUntil.After(base) {
		t.Fatalf("the newest ban should have been kept, got %+v ok=%v", entry, ok)
	}
	if got := store.Len(); got != capacity {
		t.Fatalf("expected %d entries, got %d", capacity, got)
	}
}

func TestPeerstoreBansOfProtectedEntriesDoNotCountTowardsTheQuarter(t *testing.T) {
	const capacity = 8
	store := newTestPeerstore(t)
	store.SetLimits(capacity, 0)
	base := time.Now()
	until := base.Add(time.Hour)

	// A banned peer that is connected right now cannot be evicted, so it must
	// not count towards the share that bans may take from the others.
	for i := 0; i < 3; i++ {
		id := peerstoreEntryAt(i, base).NodeID
		if err := store.Put(peerstoreEntryAt(i, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		if err := store.SetBan(id, until); err != nil {
			t.Fatalf("ban %d: %v", i, err)
		}
		store.Protect(id)
	}
	for i := 3; i < 10*capacity; i++ {
		if err := store.Put(peerstoreEntryAt(i, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, ok := store.ByNodeID(peerstoreEntryAt(i, base).NodeID); !ok {
			t.Fatalf("protected entry %d was evicted", i)
		}
	}
	if got := store.Len(); got != capacity {
		t.Fatalf("expected %d entries, got %d", capacity, got)
	}
}

func TestReputationDoesNotLetBansCrowdOutOtherPeers(t *testing.T) {
	const capacity = 64
	m := NewReputationManager(ReputationConfig{BanScore: 100, GreyScore: 50, MaxRecords: capacity})
	now := time.Now()
	m.PenalizeMalformed("honest-peer", now, false)
	want := m.Score("honest-peer", now)
	if want == 0 {
		t.Fatalf("the penalty did not register")
	}
	for i := 0; i < 4*capacity; i++ {
		at := now.Add(time.Duration(i) * time.Millisecond)
		m.SetBan(fmt.Sprintf("banned-%04d", i), at.Add(time.Hour), at)
	}
	if got := m.Score("honest-peer", now); got != want {
		t.Fatalf("the record of a peer that is not banned was lost: score %d, want %d", got, want)
	}
	m.mu.Lock()
	tracked := len(m.records)
	m.mu.Unlock()
	if tracked > capacity {
		t.Fatalf("reputation table holds %d peers, cap is %d", tracked, capacity)
	}
	last := now.Add(4 * capacity * time.Millisecond)
	if !m.IsBanned(fmt.Sprintf("banned-%04d", 4*capacity-1), last) {
		t.Fatalf("the newest ban should have been kept")
	}
	if m.IsBanned("banned-0000", last) {
		t.Fatalf("the oldest ban should have been dropped to make room")
	}
}

func TestReputationKeepsBansUpToAQuarterOfTheTable(t *testing.T) {
	const capacity = 64
	m := NewReputationManager(ReputationConfig{BanScore: 100, GreyScore: 50, MaxRecords: capacity})
	now := time.Now()
	until := now.Add(time.Hour)

	// Within the quarter, every ban survives a long run of new peers.
	const within = capacity/4 - 6
	for i := 0; i < within; i++ {
		m.SetBan(fmt.Sprintf("early-ban-%02d", i), until, now.Add(time.Duration(i)*time.Millisecond))
	}
	for i := 0; i < 10*capacity; i++ {
		m.MarkUseful(fmt.Sprintf("peer-%04d", i), now.Add(time.Second+time.Duration(i)*time.Millisecond))
	}
	if got := countBannedReputationRecords(m, now); got != within {
		t.Fatalf("expected the %d bans within the quarter to survive, %d did", within, got)
	}

	// Beyond the quarter, the oldest bans go first and the quarter is kept.
	for i := 0; i < 40; i++ {
		m.SetBan(fmt.Sprintf("late-ban-%02d", i), until, now.Add(time.Minute+time.Duration(i)*time.Millisecond))
	}
	for i := 0; i < capacity; i++ {
		m.MarkUseful(fmt.Sprintf("later-peer-%04d", i), now.Add(2*time.Minute+time.Duration(i)*time.Millisecond))
	}
	if got := countBannedReputationRecords(m, now); got != capacity/4 {
		t.Fatalf("expected the bans to be cut down to %d, %d remain", capacity/4, got)
	}
	if !m.IsBanned("late-ban-39", now) {
		t.Fatalf("the newest ban should have been kept")
	}
	if m.IsBanned("early-ban-00", now) || m.IsBanned("late-ban-00", now) {
		t.Fatalf("the oldest bans should have been dropped first")
	}
	m.mu.Lock()
	tracked := len(m.records)
	m.mu.Unlock()
	if tracked != capacity {
		t.Fatalf("expected %d records, got %d", capacity, tracked)
	}
}
