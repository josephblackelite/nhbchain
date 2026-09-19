package core

import (
	"container/list"
	"sync"
	"time"
)

// This file holds the proposer-local "strike book": a bounded record of the
// transactions that failed while this node was assembling a block proposal
// and are still resident in its mempool.
//
// Nothing in here is consensus state. It is never persisted, never gossiped,
// never read by ValidateBlock/commitBlock, and losing an entry (restart, LRU
// overflow) can only delay the eviction of a bad transaction -- containment
// of a bad transaction inside CreateBlock never depends on the book (see
// core/proposal_containment.go). Its two jobs are:
//
//  1. Backoff: a transaction that just failed is not re-offered to the very
//     next proposal attempt at the same height (or, after repeated failures,
//     for a few heights), so a pile of transactions that cannot apply stops
//     displacing eligible ones at the head of the schedule.
//  2. Bounded lifetime: a transaction that keeps failing is evicted from the
//     local mempool -- after enough strikes, once a solo dry run confirms it
//     fails even on its own against committed state (see confirmSoloFailure),
//     or after a wall-clock TTL for the designed-transient "skip" class.
//
// Lock order: the book's mutex is a LEAF. Callers may hold Node.mempoolMu (or
// Node.stateMu) when calling in, but the book never calls out while holding
// its own mutex.

const (
	// defaultStrikeBookCapacity bounds the number of tracked transactions. It
	// is twice config.DefaultMempoolMaxTransactions rounded up: live records
	// are bounded by the mempool itself (records are dropped whenever a
	// transaction leaves the mempool), so the cap only matters as a hard
	// backstop, and about 250 bytes per record keeps the worst case near 2 MB.
	defaultStrikeBookCapacity = 8192

	// maxStrikeErrorLen caps the stored error text. It is only ever logged.
	maxStrikeErrorLen = 160
)

// skipBackoffHeights is the height delay applied after the Nth designed-
// transient skip of the same transaction (index skips-1, clamped to the last
// entry). The first two skips carry no delay, so an ordinary pause or cap that
// clears on the next block costs nothing; a transaction that keeps getting
// skipped backs off geometrically up to 32 heights.
var skipBackoffHeights = [...]uint64{0, 0, 2, 4, 8, 16, 32}

// txStrikeRecord is the per-transaction failure history. Fields are guarded
// by txStrikeBook.mu; callers only ever see copies.
type txStrikeRecord struct {
	strikes    uint8     // quarantine / nondeterministic / validation failures
	skips      uint16    // designed-transient skips (no strike)
	firstFail  time.Time // proposer-local wall clock; used only for TTLs
	lastFail   time.Time
	retryAfter uint64 // height: excluded from GetMempool while retryAfter > nextHeight
	lastErr    string
	elem       *list.Element
}

// txStrikeBook is the bounded LRU map described above, keyed by
// transactionKey (hash plus sender, or hash alone for senderless types).
type txStrikeBook struct {
	mu                sync.Mutex
	capacity          int
	records           map[string]*txStrikeRecord
	lru               *list.List // front = most recently touched; values are keys
	overflowEvictions uint64
	reportedOverflow  uint64
}

func newTxStrikeBook(capacity int) *txStrikeBook {
	if capacity <= 0 {
		capacity = defaultStrikeBookCapacity
	}
	return &txStrikeBook{
		capacity: capacity,
		records:  make(map[string]*txStrikeRecord),
		lru:      list.New(),
	}
}

func truncateStrikeError(text string) string {
	if len(text) <= maxStrikeErrorLen {
		return text
	}
	return text[:maxStrikeErrorLen]
}

// touchLocked returns the record for key, creating it (and evicting the least
// recently touched record if the book is full) when absent.
func (b *txStrikeBook) touchLocked(key string, now time.Time) *txStrikeRecord {
	if rec, ok := b.records[key]; ok {
		b.lru.MoveToFront(rec.elem)
		return rec
	}
	for len(b.records) >= b.capacity {
		oldest := b.lru.Back()
		if oldest == nil {
			break
		}
		oldKey := oldest.Value.(string)
		b.lru.Remove(oldest)
		delete(b.records, oldKey)
		b.overflowEvictions++
	}
	rec := &txStrikeRecord{firstFail: now}
	rec.elem = b.lru.PushFront(key)
	b.records[key] = rec
	return rec
}

// noteStrike records one quarantine-class failure of key while building the
// block at height and returns the total number of strikes now held. The
// transaction is held back for one further height after its second strike
// (retryAfter = height+2) and is eligible again on the very next height after
// its first (retryAfter = height+1) -- see Node.GetMempool's gate.
func (b *txStrikeBook) noteStrike(key string, height uint64, now time.Time, errText string) int {
	if b == nil || key == "" {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	rec := b.touchLocked(key, now)
	if rec.strikes < 255 {
		rec.strikes++
	}
	rec.lastFail = now
	rec.lastErr = truncateStrikeError(errText)
	delay := uint64(rec.strikes)
	if delay > 2 {
		delay = 2
	}
	rec.retryAfter = height + delay
	return int(rec.strikes)
}

// noteSkip records one designed-transient skip of key (a pause, a rolling cap,
// a registry entry that can still change). It carries no strike. It returns
// the total number of skips held.
func (b *txStrikeBook) noteSkip(key string, height uint64, now time.Time, errText string) int {
	if b == nil || key == "" {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	rec := b.touchLocked(key, now)
	if rec.skips < 65535 {
		rec.skips++
	}
	rec.lastFail = now
	rec.lastErr = truncateStrikeError(errText)
	idx := int(rec.skips) - 1
	if idx >= len(skipBackoffHeights) {
		idx = len(skipBackoffHeights) - 1
	}
	rec.retryAfter = height + skipBackoffHeights[idx]
	return int(rec.skips)
}

// resetStrikes clears the strike count for key while keeping its history. It
// is used when a solo dry run shows the transaction is not itself faulty (it
// only lost a same-block conflict), so it must not be evicted by strikes.
func (b *txStrikeBook) resetStrikes(key string) {
	if b == nil || key == "" {
		return
	}
	b.mu.Lock()
	if rec, ok := b.records[key]; ok {
		rec.strikes = 0
	}
	b.mu.Unlock()
}

// forget drops any record for key. Called wherever a transaction leaves the
// mempool (commit, drop, replacement, trimming) and when it applies cleanly.
func (b *txStrikeBook) forget(key string) {
	if b == nil || key == "" {
		return
	}
	b.mu.Lock()
	if rec, ok := b.records[key]; ok {
		b.lru.Remove(rec.elem)
		delete(b.records, key)
	}
	b.mu.Unlock()
}

// forgetAll drops the records for every key in keys.
func (b *txStrikeBook) forgetAll(keys []string) {
	if b == nil || len(keys) == 0 {
		return
	}
	b.mu.Lock()
	for _, key := range keys {
		if rec, ok := b.records[key]; ok {
			b.lru.Remove(rec.elem)
			delete(b.records, key)
		}
	}
	b.mu.Unlock()
}

// peek returns a copy of the record for key without touching LRU order.
func (b *txStrikeBook) peek(key string) (txStrikeRecord, bool) {
	if b == nil {
		return txStrikeRecord{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.records[key]
	if !ok {
		return txStrikeRecord{}, false
	}
	cp := *rec
	cp.elem = nil
	return cp, true
}

// sweep drops every record whose key is not in live and returns how many were
// dropped. It is the safety net for any mempool exit path that forgot to call
// forget.
func (b *txStrikeBook) sweep(live map[string]struct{}) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	removed := 0
	for key, rec := range b.records {
		if _, ok := live[key]; ok {
			continue
		}
		b.lru.Remove(rec.elem)
		delete(b.records, key)
		removed++
	}
	return removed
}

func (b *txStrikeBook) size() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.records)
}

func (b *txStrikeBook) overflowCount() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflowEvictions
}

// takeOverflow returns the number of overflow evictions since the previous
// call, so a caller can feed a monotonic counter metric.
func (b *txStrikeBook) takeOverflow() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delta := b.overflowEvictions - b.reportedOverflow
	b.reportedOverflow = b.overflowEvictions
	return delta
}

// gateVerdict is the book's answer to "may this transaction be offered to the
// next proposal attempt?".
type gateVerdict int

const (
	gateOK      gateVerdict = iota // offer it
	gateBackoff                    // hold it back: retryAfter is still in the future
	gateExpired                    // evict it: a TTL elapsed
)

// gate decides whether key may be offered while building the block at
// nextHeight. Transactions with no record always pass, so a transaction that
// simply has not had its turn is never held back or evicted here.
//
// Two TTLs apply, both measured from the first recorded failure: skipTTL to
// records that have only ever been skipped (designed-transient causes such as
// a long pause), and absTTL to any record at all. A zero TTL disables that
// check. reason is a short label for logs and metrics when the verdict is
// gateExpired.
func (b *txStrikeBook) gate(key string, nextHeight uint64, now time.Time, skipTTL, absTTL time.Duration) (verdict gateVerdict, reason string) {
	if b == nil || key == "" {
		return gateOK, ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.records[key]
	if !ok {
		return gateOK, ""
	}
	age := now.Sub(rec.firstFail)
	if absTTL > 0 && age > absTTL {
		return gateExpired, "absolute_ttl"
	}
	if skipTTL > 0 && rec.strikes == 0 && rec.skips > 0 && age > skipTTL {
		return gateExpired, "skip_ttl"
	}
	if rec.retryAfter > nextHeight {
		return gateBackoff, ""
	}
	return gateOK, ""
}
