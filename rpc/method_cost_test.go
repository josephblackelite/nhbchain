package rpc

import (
	"testing"
	"time"
)

// costTestConfig sizes every limiter at 100 calls per window so that weights of
// 10 divide it evenly, and adds the weights under test.
func costTestConfig(costs map[string]int) ServerConfig {
	return ServerConfig{
		MaxTxPerWindow: 100, MaxTxPerIP: 100, MaxTxPerIdentity: 100, MaxTxPerChain: 100, MaxTxPerIdentityChain: 100,
		MethodCosts: costs,
	}
}

// countAllowed calls allowSource for one method from one address until it refuses,
// and returns how many calls it let through (capped, so a broken limiter cannot hang
// the test).
func countAllowed(s *Server, source, method string, now time.Time, cap int) int {
	n := 0
	for n < cap && s.allowSource(source, "", "", method, now) {
		n++
	}
	return n
}

// A heavy read and a balance lookup used to take the same one token from a caller's
// allowance, so a caller within its request count could pick only the expensive
// reads (PL-DC-48). A configured weight makes a call take that many tokens. With no
// weights configured (the default) nothing changes.
func TestMethodCostsDefaultToOneToken(t *testing.T) {
	s := newTestServer(t, nil, nil, costTestConfig(nil))
	limit := s.rateLimitMax[limiterScopeIP][""]
	if limit <= 0 {
		t.Fatalf("no per-IP limit in the test server")
	}
	got := countAllowed(s, "203.0.113.20", "nhb_getExplorerSnapshot", time.Now(), limit+50)
	if got != limit {
		t.Fatalf("with no weights configured a method must get exactly its limit (%d), got %d", limit, got)
	}
}

func TestMethodCostWeightsReduceAHeavyMethodsAllowance(t *testing.T) {
	s := newTestServer(t, nil, nil, costTestConfig(map[string]int{"nhb_getExplorerSnapshot": 10}))
	limit := s.rateLimitMax[limiterScopeIP][""]
	now := time.Now()
	heavy := countAllowed(s, "203.0.113.21", "nhb_getExplorerSnapshot", now, limit+50)
	if want := limit / 10; heavy != want {
		t.Fatalf("a method weighted 10 must get limit/10 = %d calls, got %d", want, heavy)
	}
	// The light method keeps its own full allowance from the same address.
	light := countAllowed(s, "203.0.113.21", rateLimitTestMethod, now, limit+50)
	if light != limit {
		t.Fatalf("an unweighted method must keep its full allowance %d, got %d", limit, light)
	}
}

func TestMethodCostIsClampedToTheLimitAndIgnoresNonsense(t *testing.T) {
	s := newTestServer(t, nil, nil, costTestConfig(map[string]int{
		"nhb_getExplorerSnapshot": 1_000_000, // above any limit: clamped, so one call per window still works
		"nhb_getTransaction":      0,         // not a weight: ignored
		"nhb_searchExplorer":      -5,        // not a weight: ignored
		"  ":                      3,         // no method name: ignored
	}))
	limit := s.rateLimitMax[limiterScopeIP][""]
	now := time.Now()
	if got := countAllowed(s, "203.0.113.22", "nhb_getExplorerSnapshot", now, 5); got != 1 {
		t.Fatalf("a weight above the limit must be clamped to the limit: one call per window, got %d", got)
	}
	if got := countAllowed(s, "203.0.113.23", "nhb_getTransaction", now, limit+5); got != limit {
		t.Fatalf("a zero weight must be ignored, got %d calls, want %d", got, limit)
	}
	if got := countAllowed(s, "203.0.113.24", "nhb_searchExplorer", now, limit+5); got != limit {
		t.Fatalf("a negative weight must be ignored, got %d calls, want %d", got, limit)
	}
}

// A refused call must not spend tokens: the weight is checked against the bucket
// before anything is taken, so a caller who is refused can try again as the bucket
// refills.
func TestMethodCostRefusalSpendsNothing(t *testing.T) {
	s := newTestServer(t, nil, nil, costTestConfig(map[string]int{"nhb_getExplorerSnapshot": 10}))
	limit := s.rateLimitMax[limiterScopeIP][""]
	now := time.Now()
	allowed := countAllowed(s, "203.0.113.25", "nhb_getExplorerSnapshot", now, limit)
	left := limit - allowed*10
	if left < 0 || left >= 10 {
		t.Fatalf("unexpected tokens left %d after %d weighted calls", left, allowed)
	}
	// Repeated refusals must not eat the remainder; after the window refills, the
	// full allowance is back.
	for i := 0; i < 20; i++ {
		if s.allowSource("203.0.113.25", "", "", "nhb_getExplorerSnapshot", now) {
			t.Fatalf("a refused caller got through")
		}
	}
	later := now.Add(s.rateLimitWindow + time.Second)
	if again := countAllowed(s, "203.0.113.25", "nhb_getExplorerSnapshot", later, limit); again != allowed {
		t.Fatalf("after the window the allowance must be back to %d weighted calls, got %d", allowed, again)
	}
}

// When the bucket holds some tokens but fewer than the call's weight, the call must
// be refused: a limit of 25 with a weight of 10 allows two calls and refuses the
// third, which would see five tokens.
func TestMethodCostRefusesWhenTheBucketHoldsLessThanTheWeight(t *testing.T) {
	cfg := ServerConfig{
		MaxTxPerWindow: 25, MaxTxPerIP: 25, MaxTxPerIdentity: 25, MaxTxPerChain: 25, MaxTxPerIdentityChain: 25,
		MethodCosts: map[string]int{"nhb_getExplorerSnapshot": 10},
	}
	s := newTestServer(t, nil, nil, cfg)
	if got := countAllowed(s, "203.0.113.30", "nhb_getExplorerSnapshot", time.Now(), 10); got != 2 {
		t.Fatalf("a limit of 25 with a weight of 10 allows exactly 2 calls, got %d", got)
	}
}

// A name the server does not serve shares one bucket per source and cannot be given
// a weight: otherwise a caller could pick the weight by inventing the name.
func TestMethodCostCannotBeAppliedToAnUnservedName(t *testing.T) {
	s := newTestServer(t, nil, nil, costTestConfig(map[string]int{"zz_no_such_method": 10}))
	limit := s.rateLimitMax[limiterScopeIP][""]
	if got := countAllowed(s, "203.0.113.31", "zz_no_such_method", time.Now(), limit+5); got != limit {
		t.Fatalf("an unserved name must keep the ordinary one-token cost: got %d calls, want %d", got, limit)
	}
}
