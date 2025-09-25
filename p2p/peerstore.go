package p2p

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
)

const (
	defaultBaseBackoff = time.Second
	defaultMaxBackoff  = 30 * time.Minute
)

// PeerstoreEntry captures the dial metadata we persist for each peer.
type PeerstoreEntry struct {
	Addr        string    `json:"addr"`
	NodeID      string    `json:"nodeID"`
	Score       float64   `json:"score"`
	LastSeen    time.Time `json:"lastSeen"`
	Fails       int       `json:"fails"`
	BannedUntil time.Time `json:"bannedUntil"`
}

// Peerstore offers a concurrency-safe persistent registry of peer metadata.
type Peerstore struct {
	mu sync.RWMutex

	db *leveldb.DB

	byAddr map[string]*PeerstoreEntry
	byNode map[string]*PeerstoreEntry

	baseBackoff time.Duration
	maxBackoff  time.Duration
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
	}
	if err := store.load(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
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
	rec.Score += 1
	if rec.Score > 1000 {
		rec.Score = 1000
	}
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
		if existing.Addr != "" && existing.Addr != rec.Addr {
			delete(ps.byAddr, existing.Addr)
		}
	} else if rec.LastSeen.IsZero() {
		rec.LastSeen = time.Now()
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
	blob, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := []byte("peer:" + rec.NodeID)
	return ps.db.Put(key, blob, nil)
}

func (ps *Peerstore) load() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	iter := ps.db.NewIterator(nil, nil)
	defer iter.Release()
	for iter.Next() {
		key := string(iter.Key())
		if len(key) < 5 || key[:5] != "peer:" {
			continue
		}
		var rec PeerstoreEntry
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			return fmt.Errorf("decode peer %s: %w", key, err)
		}
		copy := rec
		ps.byNode[rec.NodeID] = &copy
		if rec.Addr != "" {
			ps.byAddr[rec.Addr] = &copy
		}
	}
	return iter.Error()
}
