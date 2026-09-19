package p2p

import (
	"container/heap"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
)

const (
	defaultBaseBackoff = time.Second
	defaultMaxBackoff  = 30 * time.Minute

	peerstoreMaxScore     = 1000.0
	peerstoreMinScore     = -100.0
	violationScorePenalty = 10.0

	// The peerstore is fed, directly or indirectly, by remote peers, so it is
	// bounded in every dimension: number of entries, age of an entry and the
	// size of each persisted record. Entries beyond the cap are evicted least
	// recently seen first; entries not seen for the TTL are pruned.
	defaultPeerstoreMaxEntries = 2048
	defaultPeerstoreTTL        = 14 * 24 * time.Hour
	maxPeerstoreRecordBytes    = 4096
	maxPeerstoreAddrLen        = 255
	maxPeerstoreNodeIDLen      = 128
)

var errPeerstoreFull = errors.New("peerstore full")

// PeerstoreEntry captures the dial metadata we persist for each peer.
type PeerstoreEntry struct {
	Addr          string    `json:"addr"`
	NodeID        string    `json:"nodeID"`
	Score         float64   `json:"score"`
	LastSeen      time.Time `json:"lastSeen"`
	Fails         int       `json:"fails"`
	BannedUntil   time.Time `json:"bannedUntil"`
	Violations    int       `json:"violations,omitempty"`
	LastViolation time.Time `json:"lastViolation,omitempty"`
}

// Peerstore offers a concurrency-safe persistent registry of peer metadata.
type Peerstore struct {
	mu sync.RWMutex

	db *leveldb.DB

	byAddr map[string]*PeerstoreEntry
	byNode map[string]*PeerstoreEntry

	baseBackoff time.Duration
	maxBackoff  time.Duration

	// maxEntries and ttl bound the store (zero disables the respective bound).
	// protected holds the node IDs that must survive eviction and pruning, such
	// as the peers we are connected to right now.
	maxEntries int
	ttl        time.Duration
	protected  map[string]struct{}
}

// NewPeerstore opens (or creates) a peerstore backed by LevelDB at the given path.
func NewPeerstore(path string, baseBackoff, maxBackoff time.Duration) (*Peerstore, error) {
	if path == "" {
		return nil, errors.New("peerstore path required")
	}
	if baseBackoff <= 0 {
		baseBackoff = defaultBaseBackoff
	}
	if maxBackoff <= 0 {
		maxBackoff = defaultMaxBackoff
	}
	db, err := leveldb.OpenFile(filepath.Clean(path), nil)
	if err != nil {
		return nil, fmt.Errorf("open peerstore: %w", err)
	}

	store := &Peerstore{
		db:          db,
		byAddr:      make(map[string]*PeerstoreEntry),
		byNode:      make(map[string]*PeerstoreEntry),
		baseBackoff: baseBackoff,
		maxBackoff:  maxBackoff,
		maxEntries:  defaultPeerstoreMaxEntries,
		ttl:         defaultPeerstoreTTL,
		protected:   make(map[string]struct{}),
	}
	if err := store.load(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// SetLimits changes the entry cap and the time-to-live of unseen entries (zero
// disables the respective bound) and immediately trims the store to fit.
func (ps *Peerstore) SetLimits(maxEntries int, ttl time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.maxEntries = maxEntries
	ps.ttl = ttl
	if ps.byNode == nil {
		return
	}
	for ps.maxEntries > 0 && len(ps.byNode) > ps.maxEntries {
		victim := ps.pickVictimLocked(time.Now())
		if victim == "" {
			break
		}
		ps.deleteLocked(victim)
	}
}

// Len reports how many peers the store currently holds.
func (ps *Peerstore) Len() int {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return len(ps.byNode)
}

// Protect exempts a node from eviction and pruning until Unprotect is called.
func (ps *Peerstore) Protect(nodeID string) {
	if nodeID == "" {
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.protected == nil {
		ps.protected = make(map[string]struct{})
	}
	ps.protected[nodeID] = struct{}{}
}

// Unprotect makes a node subject to eviction and pruning again.
func (ps *Peerstore) Unprotect(nodeID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.protected, nodeID)
}

// Prune drops every unprotected, unbanned entry that has not been seen for the
// time-to-live and reports how many were removed.
func (ps *Peerstore) Prune(now time.Time) int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.ttl <= 0 {
		return 0
	}
	removed := 0
	for nodeID, rec := range ps.byNode {
		if _, keep := ps.protected[nodeID]; keep || rec.BannedUntil.After(now) {
			continue
		}
		if now.Sub(rec.LastSeen) > ps.ttl {
			ps.deleteLocked(nodeID)
			removed++
		}
	}
	return removed
}

// Close flushes and closes the underlying database.
func (ps *Peerstore) Close() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.db == nil {
		return nil
	}
	err := ps.db.Close()
	ps.db = nil
	ps.byAddr = nil
	ps.byNode = nil
	return err
}

// Put inserts or updates a record keyed by node ID, deduplicating addresses.
func (ps *Peerstore) Put(rec PeerstoreEntry) error {
	if rec.NodeID == "" {
		return errors.New("nodeID required")
	}
	if len(rec.NodeID) > maxPeerstoreNodeIDLen || len(rec.Addr) > maxPeerstoreAddrLen {
		return errors.New("peerstore record too large")
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.putLocked(&rec)
}

// Get returns a record by address.
func (ps *Peerstore) Get(addr string) (PeerstoreEntry, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	rec := ps.byAddr[addr]
	if rec == nil {
		return PeerstoreEntry{}, false
	}
	return *rec, true
}

// ByNodeID returns a record by node identifier.
func (ps *Peerstore) ByNodeID(nodeID string) (PeerstoreEntry, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	rec := ps.byNode[nodeID]
	if rec == nil {
		return PeerstoreEntry{}, false
	}
	return *rec, true
}

// RecordSuccess updates score bookkeeping for a successful interaction.
func (ps *Peerstore) RecordSuccess(nodeID string, now time.Time) (PeerstoreEntry, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	rec := ps.byNode[nodeID]
	if rec == nil {
		return PeerstoreEntry{}, fmt.Errorf("record success: %w", leveldb.ErrNotFound)
	}
	rec.Score = clampScore(rec.Score + 1)
	rec.LastSeen = now
	rec.Fails = 0
	if rec.BannedUntil.After(now) {
		rec.BannedUntil = time.Time{}
	}
	if err := ps.persistLocked(rec); err != nil {
		return PeerstoreEntry{}, err
	}
	return *rec, nil
}

// RecordFail increases failure counters and applies exponential backoff decay to score.
func (ps *Peerstore) RecordFail(nodeID string, now time.Time) (PeerstoreEntry, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	rec := ps.byNode[nodeID]
	if rec == nil {
		return PeerstoreEntry{}, fmt.Errorf("record fail: %w", leveldb.ErrNotFound)
	}
	rec.Fails++
	rec.LastSeen = now
	if rec.Score > 0 {
		rec.Score *= 0.5
		if rec.Score < 0.001 {
			rec.Score = 0
		}
	}
	rec.Score = clampScore(rec.Score)
	if err := ps.persistLocked(rec); err != nil {
		return PeerstoreEntry{}, err
	}
	return *rec, nil
}

// RecordViolation increments the violation counter and penalizes the peer's score.
func (ps *Peerstore) RecordViolation(nodeID string, now time.Time) (PeerstoreEntry, error) {
	if nodeID == "" {
		return PeerstoreEntry{}, errors.New("nodeID required")
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()

	rec := ps.byNode[nodeID]
	if rec == nil {
		if len(nodeID) > maxPeerstoreNodeIDLen {
			return PeerstoreEntry{}, errors.New("peerstore record too large")
		}
		if !ps.makeRoomLocked(now) {
			return PeerstoreEntry{}, errPeerstoreFull
		}
		rec = &PeerstoreEntry{NodeID: nodeID}
		ps.byNode[nodeID] = rec
	}
	rec.Violations++
	rec.LastViolation = now
	rec.LastSeen = now
	rec.Score = clampScore(rec.Score - violationScorePenalty)
	if rec.Addr != "" {
		ps.byAddr[rec.Addr] = rec
	}
	if err := ps.persistLocked(rec); err != nil {
		return PeerstoreEntry{}, err
	}
	return *rec, nil
}

// SetBan sets a ban expiry timestamp for a peer.
func (ps *Peerstore) SetBan(nodeID string, until time.Time) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	rec := ps.byNode[nodeID]
	if rec == nil {
		return fmt.Errorf("set ban: %w", leveldb.ErrNotFound)
	}
	rec.BannedUntil = until
	if err := ps.persistLocked(rec); err != nil {
		return err
	}
	return nil
}

// IsBanned reports whether the peer is currently banned.
func (ps *Peerstore) IsBanned(nodeID string, now time.Time) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	rec := ps.byNode[nodeID]
	if rec == nil {
		return false
	}
	if rec.BannedUntil.After(now) {
		return true
	}
	if rec.BannedUntil.IsZero() {
		return false
	}
	rec.BannedUntil = time.Time{}
	_ = ps.persistLocked(rec)
	return false
}

// NextDialAt returns when we should attempt to dial the address next based on backoff.
func (ps *Peerstore) NextDialAt(addr string, now time.Time) time.Time {
	ps.mu.RLock()
	rec := ps.byAddr[addr]
	if rec == nil {
		ps.mu.RUnlock()
		return now
	}
	snapshot := *rec
	ps.mu.RUnlock()
	if snapshot.BannedUntil.After(now) {
		return snapshot.BannedUntil
	}
	if snapshot.Fails <= 0 {
		if snapshot.LastSeen.After(now) {
			return snapshot.LastSeen
		}
		return now
	}
	base := ps.baseBackoff
	if base <= 0 {
		base = defaultBaseBackoff
	}
	factor := time.Duration(1)
	if snapshot.Fails > 1 {
		factor = 1 << uint(snapshot.Fails-1)
	}
	backoff := base * factor
	if ps.maxBackoff > 0 && backoff > ps.maxBackoff {
		backoff = ps.maxBackoff
	}
	next := snapshot.LastSeen.Add(backoff)
	if next.Before(now) {
		return now
	}
	return next
}

// Snapshot returns a copy of all peerstore entries.
func (ps *Peerstore) Snapshot() []PeerstoreEntry {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if len(ps.byNode) == 0 {
		return nil
	}
	entries := make([]PeerstoreEntry, 0, len(ps.byNode))
	for _, rec := range ps.byNode {
		if rec == nil {
			continue
		}
		entries = append(entries, *rec)
	}
	return entries
}

func (ps *Peerstore) putLocked(rec *PeerstoreEntry) error {
	existing := ps.byNode[rec.NodeID]
	if existing != nil {
		if rec.Addr == "" {
			rec.Addr = existing.Addr
		}
		if rec.Score == 0 {
			rec.Score = existing.Score
		}
		if rec.LastSeen.IsZero() {
			rec.LastSeen = existing.LastSeen
		}
		if rec.Fails == 0 {
			rec.Fails = existing.Fails
		}
		if rec.BannedUntil.IsZero() {
			rec.BannedUntil = existing.BannedUntil
		}
		if rec.Violations == 0 {
			rec.Violations = existing.Violations
		}
		if rec.LastViolation.IsZero() {
			rec.LastViolation = existing.LastViolation
		}
		if existing.Addr != "" && existing.Addr != rec.Addr {
			delete(ps.byAddr, existing.Addr)
		}
	} else {
		if rec.LastSeen.IsZero() {
			rec.LastSeen = time.Now()
		}
		if !ps.makeRoomLocked(rec.LastSeen) {
			return errPeerstoreFull
		}
	}
	copy := *rec
	ps.byNode[rec.NodeID] = &copy
	if copy.Addr != "" {
		ps.byAddr[copy.Addr] = &copy
	}
	if err := ps.persistLocked(&copy); err != nil {
		return err
	}
	return nil
}

func (ps *Peerstore) persistLocked(rec *PeerstoreEntry) error {
	if ps.db == nil {
		return errors.New("peerstore closed")
	}
	rec.Score = clampScore(rec.Score)
	blob, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := []byte("peer:" + rec.NodeID)
	return ps.db.Put(key, blob, nil)
}

func clampScore(value float64) float64 {
	if value > peerstoreMaxScore {
		return peerstoreMaxScore
	}
	if value < peerstoreMinScore {
		return peerstoreMinScore
	}
	return value
}

// pickVictimLocked chooses the entry to evict: the least recently seen one that
// is neither protected nor under an active ban. Entries under an active ban are
// only chosen when nothing else is left. It returns "" when every entry is
// protected.
func (ps *Peerstore) pickVictimLocked(now time.Time) string {
	victim, banned := "", ""
	var victimSeen, bannedSeen time.Time
	for nodeID, rec := range ps.byNode {
		if _, keep := ps.protected[nodeID]; keep {
			continue
		}
		if rec.BannedUntil.After(now) {
			if banned == "" || rec.LastSeen.Before(bannedSeen) || (rec.LastSeen.Equal(bannedSeen) && nodeID < banned) {
				banned, bannedSeen = nodeID, rec.LastSeen
			}
			continue
		}
		if victim == "" || rec.LastSeen.Before(victimSeen) || (rec.LastSeen.Equal(victimSeen) && nodeID < victim) {
			victim, victimSeen = nodeID, rec.LastSeen
		}
	}
	if victim == "" {
		return banned
	}
	return victim
}

// makeRoomLocked evicts entries until one more can be added. It reports false
// when the store is full of protected entries.
func (ps *Peerstore) makeRoomLocked(now time.Time) bool {
	if ps.maxEntries <= 0 {
		return true
	}
	for len(ps.byNode) >= ps.maxEntries {
		victim := ps.pickVictimLocked(now)
		if victim == "" {
			return false
		}
		ps.deleteLocked(victim)
	}
	return true
}

func (ps *Peerstore) deleteLocked(nodeID string) {
	rec := ps.byNode[nodeID]
	if rec == nil {
		return
	}
	delete(ps.byNode, nodeID)
	if rec.Addr != "" && ps.byAddr[rec.Addr] == rec {
		delete(ps.byAddr, rec.Addr)
	}
	if ps.db != nil {
		_ = ps.db.Delete([]byte("peer:"+nodeID), nil)
	}
}

// peerstoreLoadItem and peerstoreLoadHeap keep the most recently seen records
// while the database is scanned, so that load never holds more than the cap.
type peerstoreLoadItem struct {
	key string
	rec PeerstoreEntry
}

type peerstoreLoadHeap []peerstoreLoadItem

func (h peerstoreLoadHeap) Len() int           { return len(h) }
func (h peerstoreLoadHeap) Less(i, j int) bool { return h[i].rec.LastSeen.Before(h[j].rec.LastSeen) }
func (h peerstoreLoadHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *peerstoreLoadHeap) Push(x any)        { *h = append(*h, x.(peerstoreLoadItem)) }
func (h *peerstoreLoadHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// load reads the persisted records into memory. It keeps at most maxEntries of
// them (the most recently seen) and skips, and deletes from disk, records that
// are expired, oversized or undecodable, so that neither a crowded database nor
// a single damaged record can keep the node from starting.
func (ps *Peerstore) load() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	now := time.Now()
	iter := ps.db.NewIterator(nil, nil)
	defer iter.Release()

	keep := &peerstoreLoadHeap{}
	batch := new(leveldb.Batch)
	dropped := 0
	flush := func() error {
		if batch.Len() == 0 {
			return nil
		}
		err := ps.db.Write(batch, nil)
		batch.Reset()
		return err
	}
	drop := func(key string) error {
		batch.Delete([]byte(key))
		dropped++
		if batch.Len() >= 512 {
			return flush()
		}
		return nil
	}

	for iter.Next() {
		key := string(iter.Key())
		if len(key) < 5 || key[:5] != "peer:" {
			continue
		}
		value := iter.Value()
		var rec PeerstoreEntry
		if len(value) > maxPeerstoreRecordBytes || json.Unmarshal(value, &rec) != nil ||
			rec.NodeID == "" || key[5:] != rec.NodeID ||
			len(rec.NodeID) > maxPeerstoreNodeIDLen || len(rec.Addr) > maxPeerstoreAddrLen {
			if err := drop(key); err != nil {
				return err
			}
			continue
		}
		if ps.ttl > 0 && now.Sub(rec.LastSeen) > ps.ttl && !rec.BannedUntil.After(now) {
			if err := drop(key); err != nil {
				return err
			}
			continue
		}
		heap.Push(keep, peerstoreLoadItem{key: key, rec: rec})
		if ps.maxEntries > 0 && keep.Len() > ps.maxEntries {
			oldest := heap.Pop(keep).(peerstoreLoadItem)
			if err := drop(oldest.key); err != nil {
				return err
			}
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	if dropped > 0 {
		slog.Default().Warn("Pruned peerstore records while loading",
			slog.Int("dropped", dropped),
			slog.Int("kept", keep.Len()))
	}
	for _, item := range *keep {
		copy := item.rec
		ps.byNode[copy.NodeID] = &copy
		if copy.Addr != "" {
			ps.byAddr[copy.Addr] = &copy
		}
	}
	return nil
}
