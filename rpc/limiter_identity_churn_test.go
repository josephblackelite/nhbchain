package rpc

import (
	"fmt"
	"testing"
	"time"
)

// Bug-bounty report of 2026-11-19 (ledger PL-EXT-55): the per-identity quota applies, but when
// the limiter table is full and no entry is idle, the fallback dropped the least recently seen
// busy entry, which is the caller's own drained identity bucket once the table is filled with
// other busy buckets; the next call under that identity then got a fresh bucket. An identity
// bucket is bounded by the number of tokens the server has issued, so it is the entry that
// must survive the churn of source-address buckets, which anyone can create.

func newIdentityChurnServer(t *testing.T) *Server {
	t.Helper()
	return newTestServer(t, nil, nil, ServerConfig{
		MaxTxPerIP:       10,
		MaxTxPerIdentity: 2,
	})
}

func drainIdentity(t *testing.T, server *Server, identity string, now time.Time) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if !server.allowSource("203.0.113.10", identity, "", rateLimitTestMethod, now) {
			t.Fatalf("request %d under the identity quota must be allowed", i)
		}
	}
	if server.allowSource("203.0.113.10", identity, "", rateLimitTestMethod, now) {
		t.Fatalf("the third request under an identity quota of 2 must be refused")
	}
}

func TestFullLimiterTableDoesNotResetADrainedIdentityBucket(t *testing.T) {
	server := newIdentityChurnServer(t)
	now := time.Now()
	drainIdentity(t, server, "victim@example.com", now)

	// Fill the table with busy buckets of other sources (one request each, so none is idle
	// inside the refill window), more than the table holds.
	for i := 0; i < rateLimiterMaxEntries+100; i++ {
		at := now.Add(time.Duration(i+1) * time.Microsecond)
		if !server.allowSource(fmt.Sprintf("198.51.100.%d-%d", i%250, i), "", "", rateLimitTestMethod, at) {
			t.Fatalf("flood request %d must be allowed", i)
		}
	}
	server.mu.Lock()
	tracked := len(server.rateLimiters)
	server.mu.Unlock()
	if tracked != rateLimiterMaxEntries {
		t.Fatalf("the flood must fill the table (%d), tracked %d", rateLimiterMaxEntries, tracked)
	}

	if server.allowSource("203.0.113.99", "victim@example.com", "", rateLimitTestMethod, now.Add(time.Second)) {
		t.Fatalf("the drained identity got a fresh bucket after the table was filled with other sources")
	}
}

// The same holds for the identity+chain scope.
func TestFullLimiterTableDoesNotResetADrainedIdentityChainBucket(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		MaxTxPerIP:            10,
		MaxTxPerIdentity:      100,
		MaxTxPerChain:         100,
		MaxTxPerIdentityChain: 2,
	})
	now := time.Now()
	for i := 0; i < 2; i++ {
		if !server.allowSource("203.0.113.10", "victim@example.com", "chain-1:1", rateLimitTestMethod, now) {
			t.Fatalf("request %d must be allowed", i)
		}
	}
	if server.allowSource("203.0.113.10", "victim@example.com", "chain-1:1", rateLimitTestMethod, now) {
		t.Fatalf("the third request must be refused")
	}
	for i := 0; i < rateLimiterMaxEntries+100; i++ {
		at := now.Add(time.Duration(i+1) * time.Microsecond)
		if !server.allowSource(fmt.Sprintf("198.51.100.%d-%d", i%250, i), "", "", rateLimitTestMethod, at) {
			t.Fatalf("flood request %d must be allowed", i)
		}
	}
	if server.allowSource("203.0.113.99", "victim@example.com", "chain-1:1", rateLimitTestMethod, now.Add(time.Second)) {
		t.Fatalf("the drained identity+chain bucket was reset by filling the table")
	}
}

// Control, and the cost of the rule: with only 500 flood sources the table is not full and
// nothing is evicted; and when the table is full of identity buckets alone the oldest of them
// is still dropped (the table cannot grow), rather than refusing every new caller.
func TestFullLimiterTableStillMakesRoomWhenOnlyIdentityBucketsAreBusy(t *testing.T) {
	server := newIdentityChurnServer(t)
	now := time.Now()
	for i := 0; i < rateLimiterMaxEntries+10; i++ {
		identity := fmt.Sprintf("user-%d@example.com", i)
		source := fmt.Sprintf("203.0.%d.%d", i/250, i%250)
		at := now.Add(time.Duration(i) * time.Microsecond)
		for j := 0; j < 2; j++ {
			if !server.allowSource(source, identity, "", rateLimitTestMethod, at) {
				t.Fatalf("identity %d request %d must be allowed", i, j)
			}
		}
	}
	server.mu.Lock()
	tracked := len(server.rateLimiters)
	server.mu.Unlock()
	if tracked > rateLimiterMaxEntries {
		t.Fatalf("the limiter table grew past its cap: %d", tracked)
	}
}

// The fallback itself, with the table built by hand so the order of eviction is exact: when no
// entry is idle, source-address buckets go first (oldest first), identity buckets only after
// every other busy bucket is gone (also oldest first).
func TestEvictionFallbackDropsSourceBucketsBeforeIdentityBuckets(t *testing.T) {
	server := newIdentityChurnServer(t)
	now := time.Now()
	busy := func(at time.Time) *rateLimiter {
		l := &rateLimiter{lastSeen: at, limiter: newSourceRateLimiter(2, server.rateLimitWindow)}
		l.limiter.AllowN(now, 1) // one token used: not idle
		return l
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	server.rateLimiters = make(map[string]*rateLimiter)
	// identity buckets are the OLDEST entries and source buckets newer, so oldest-first alone
	// would drop the identity buckets
	server.rateLimiters[limiterKeyPrefixIdentity+"a|m"] = busy(now.Add(1 * time.Microsecond))
	server.rateLimiters[limiterKeyPrefixIdentityChain+"b|c|m"] = busy(now.Add(2 * time.Microsecond))
	server.rateLimiters[limiterKeyPrefixIP+"ip-1|m"] = busy(now.Add(10 * time.Microsecond))
	server.rateLimiters[limiterKeyPrefixIP+"ip-2|m"] = busy(now.Add(20 * time.Microsecond))
	server.rateLimiters[limiterKeyPrefixChain+"ch-1|m"] = busy(now.Add(15 * time.Microsecond))

	order := []string{
		limiterKeyPrefixIP + "ip-1|m",    // oldest source bucket
		limiterKeyPrefixChain + "ch-1|m", // next oldest
		limiterKeyPrefixIP + "ip-2|m",    // the newest source bucket still goes before any identity bucket
		limiterKeyPrefixIdentity + "a|m", // only now identity buckets, oldest first
		limiterKeyPrefixIdentityChain + "b|c|m",
	}
	for i, dropped := range order {
		before := len(server.rateLimiters)
		server.evictOldestLimiterLocked(now)
		if len(server.rateLimiters) != before-1 {
			t.Fatalf("step %d: one entry must be dropped, the table went from %d to %d", i, before, len(server.rateLimiters))
		}
		if _, still := server.rateLimiters[dropped]; still {
			t.Fatalf("step %d: expected %q to be the entry dropped", i, dropped)
		}
	}
}
