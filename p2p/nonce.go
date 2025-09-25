package p2p

import (
	"sync"
	"time"
)

type nonceGuard struct {
	window  time.Duration
	mu      sync.Mutex
	entries map[string]time.Time
}

func newNonceGuard(window time.Duration) *nonceGuard {
	if window <= 0 {
		window = 10 * time.Minute
	}
	return &nonceGuard{window: window, entries: make(map[string]time.Time)}
}

// Remember returns false if the nonce was already observed within the guard window.
func (g *nonceGuard) Remember(nonce string, observedAt time.Time) bool {
	if nonce == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	g.gcLocked(observedAt)
	if _, exists := g.entries[nonce]; exists {
		return false
	}
	g.entries[nonce] = observedAt
	return true
}

func (g *nonceGuard) gcLocked(now time.Time) {
	threshold := now.Add(-g.window)
	for nonce, seen := range g.entries {
		if seen.Before(threshold) {
			delete(g.entries, nonce)
		}
	}
}
