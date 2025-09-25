package p2p

import (
	"container/list"
	"sync"
	"time"
)

type nonceGuard struct {
	window  time.Duration
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List
}

type nonceRecord struct {
	nonce string
	seen  time.Time
}

func newNonceGuard(window time.Duration) *nonceGuard {
	if window <= 0 {
		window = 10 * time.Minute
	}
	return &nonceGuard{window: window, entries: make(map[string]*list.Element), order: list.New()}
}

// Remember returns false if the nonce was already observed within the guard window.
func (g *nonceGuard) Remember(nonce string, observedAt time.Time) bool {
	if nonce == "" {
		return false
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.pruneLocked(observedAt)
	if elem, exists := g.entries[nonce]; exists {
		if elem != nil {
			if record, ok := elem.Value.(*nonceRecord); ok && record != nil {
				record.seen = observedAt
			}
			g.order.MoveToFront(elem)
		}
		return false
	}
	record := &nonceRecord{nonce: nonce, seen: observedAt}
	elem := g.order.PushFront(record)
	g.entries[nonce] = elem
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
		delete(g.entries, record.nonce)
		elem = prev
	}
}
