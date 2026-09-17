package webhook

import (
	"sort"
	"sync"
	"time"
)

const (
	// DefaultRateLimit defines the fallback maximum number of deliveries per subscription in a window.
	DefaultRateLimit = 60

	defaultRateWindow = time.Minute
	defaultRateTTL    = 5 * time.Minute
	defaultRateCap    = 4096
)

// RateLimiter bounds webhook deliveries across rolling windows while preventing unbounded memory growth.
// It is safe for concurrent use by multiple goroutines.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[int64]rateWindow

	window time.Duration
	ttl    time.Duration
	cap    int
}

type rateWindow struct {
	windowStart time.Time
	count       int
	lastSeen    time.Time
}

// RateLimiterOption configures a RateLimiter instance.
type RateLimiterOption func(*RateLimiter)

// NewRateLimiter constructs a rate limiter with sensible defaults.
func NewRateLimiter(opts ...RateLimiterOption) *RateLimiter {
	rl := &RateLimiter{
		windows: make(map[int64]rateWindow),
		window:  defaultRateWindow,
		ttl:     defaultRateTTL,
		cap:     defaultRateCap,
	}
	for _, opt := range opts {
		opt(rl)
	}
	if rl.window <= 0 {
		rl.window = defaultRateWindow
	}
	if rl.ttl < 0 {
		rl.ttl = 0
	}
	if rl.cap < 0 {
		rl.cap = 0
	}
	return rl
}

// WithRateWindow overrides the rolling window duration used to track delivery counts.
func WithRateWindow(d time.Duration) RateLimiterOption {
	return func(rl *RateLimiter) {
		rl.window = d
	}
}

// WithRateTTL overrides the TTL used for rate window entries.
// Entries that have not been touched within the TTL are evicted.
func WithRateTTL(d time.Duration) RateLimiterOption {
	return func(rl *RateLimiter) {
		rl.ttl = d
	}
}

// WithRateCap sets the maximum number of tracked subscriptions.
func WithRateCap(cap int) RateLimiterOption {
	return func(rl *RateLimiter) {
		rl.cap = cap
	}
}

// Allow reports whether a subscription identified by id can proceed within the provided limit.
// The caller is expected to supply the current time. Limits less than or equal to zero fall back to DefaultRateLimit.
func (rl *RateLimiter) Allow(id int64, limit int, now time.Time) bool {
	if limit <= 0 {
		limit = DefaultRateLimit
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.pruneLocked(now)

	state := rl.windows[id]
	if state.windowStart.IsZero() {
		state.windowStart = now
	}
	if now.Sub(state.windowStart) >= rl.window {
		state.windowStart = now
		state.count = 0
	}
	if state.count >= limit {
		state.lastSeen = now
		rl.windows[id] = state
		return false
	}
	state.count++
	state.lastSeen = now
	rl.windows[id] = state

	if rl.cap > 0 && len(rl.windows) > rl.cap {
		rl.enforceCapLocked()
	}

	return true
}

// ResetAt returns when the current window for a subscription will reset.
// Calling ResetAt touches the rate window to keep hot subscriptions resident.
func (rl *RateLimiter) ResetAt(id int64, now time.Time) time.Time {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.pruneLocked(now)

	state := rl.windows[id]
	if state.windowStart.IsZero() {
		state.windowStart = now
	}
	state.lastSeen = now
	rl.windows[id] = state

	if rl.cap > 0 && len(rl.windows) > rl.cap {
		rl.enforceCapLocked()
	}

	return state.windowStart.Add(rl.window)
}

// Len returns the number of tracked subscriptions. Primarily for testing.
func (rl *RateLimiter) Len() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.windows)
}

func (rl *RateLimiter) pruneLocked(now time.Time) {
	if rl.ttl > 0 {
		for id, state := range rl.windows {
			if now.Sub(state.lastSeen) > rl.ttl {
				delete(rl.windows, id)
			}
		}
	}
	if rl.cap > 0 && len(rl.windows) > rl.cap {
		rl.enforceCapLocked()
	}
}

func (rl *RateLimiter) enforceCapLocked() {
	if rl.cap <= 0 || len(rl.windows) <= rl.cap {
		return
	}
	type entry struct {
		id       int64
		lastSeen time.Time
	}
	entries := make([]entry, 0, len(rl.windows))
	for id, state := range rl.windows {
		entries = append(entries, entry{id: id, lastSeen: state.lastSeen})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].lastSeen.Before(entries[j].lastSeen)
	})
	excess := len(rl.windows) - rl.cap
	for i := 0; i < excess && i < len(entries); i++ {
		delete(rl.windows, entries[i].id)
	}
}
