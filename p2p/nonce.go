package p2p

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

type nonceGuard struct {
	window  time.Duration
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List
	seen    map[string]struct{}
}

type nonceRecord struct {
	key  string
	seen time.Time
}

func newNonceGuard(window time.Duration) *nonceGuard {
	if window <= 0 {
		window = 10 * time.Minute
	}
	return &nonceGuard{
		window:  window,
		entries: make(map[string]*list.Element),
		order:   list.New(),
		seen:    make(map[string]struct{}),
	}
}

// Remember returns false if the nonce was already observed. The guard keeps a
// time-ordered window for eviction heuristics while persisting cryptographic
// fingerprints of every (nodeID, nonce) pair it has seen so that replays are
// rejected even after the window expires.
func (g *nonceGuard) Remember(nodeID, nonce string, observedAt time.Time) bool {
	if nonce == "" {
		return false
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	fingerprint := g.fingerprint(nodeID, nonce)
	if fingerprint == "" {
		return false
	}
	g.pruneLocked(observedAt)
	if _, exists := g.seen[fingerprint]; exists {
		if elem, ok := g.entries[fingerprint]; ok {
			if elem != nil {
				if record, ok := elem.Value.(*nonceRecord); ok && record != nil {
					record.seen = observedAt
				}
				g.order.MoveToFront(elem)
			}
		} else {
			record := &nonceRecord{key: fingerprint, seen: observedAt}
			elem := g.order.PushFront(record)
			g.entries[fingerprint] = elem
		}
		return false
	}
	g.seen[fingerprint] = struct{}{}
	record := &nonceRecord{key: fingerprint, seen: observedAt}
	elem := g.order.PushFront(record)
	g.entries[fingerprint] = elem
	return true
}

func (g *nonceGuard) pruneLocked(now time.Time) {
	if g.order == nil {
		return
	}
	threshold := now.Add(-g.window)
	for elem := g.order.Back(); elem != nil; {
		record, _ := elem.Value.(*nonceRecord)
		if record == nil {
			prev := elem.Prev()
			g.order.Remove(elem)
			elem = prev
			continue
		}
		if !record.seen.Before(threshold) {
			break
		}
		prev := elem.Prev()
		g.order.Remove(elem)
		delete(g.entries, record.key)
		elem = prev
	}
}

func (g *nonceGuard) fingerprint(nodeID, nonce string) string {
	trimmedNonce := strings.TrimSpace(nonce)
	if trimmedNonce == "" {
		return ""
	}
	normalized := normalizeHex(nodeID)
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimSpace(nodeID))
	}
	payload := normalized + ":" + trimmedNonce
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}
