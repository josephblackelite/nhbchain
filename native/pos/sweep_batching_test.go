package pos

import "testing"

// pendingIndexWriteCountingState counts how many times the pending-authorization
// index key (authorizationPendingIndex) is written, via either KVPut or KVDelete.
type pendingIndexWriteCountingState struct {
	*memoryLifecycleState
	writes int
}

func (s *pendingIndexWriteCountingState) KVPut(key []byte, value interface{}) error {
	if string(key) == string(authorizationPendingIndex) {
		s.writes++
	}
	return s.memoryLifecycleState.KVPut(key, value)
}

func (s *pendingIndexWriteCountingState) KVDelete(key []byte) error {
	if string(key) == string(authorizationPendingIndex) {
		s.writes++
	}
	return s.memoryLifecycleState.KVDelete(key)
}

// TestSweepExpiredRewritesThePendingIndexAtMostOnce proves PL-EXT-26: SweepExpired
// must not rewrite the whole pending-authorization index once per authorization it
// removes from it. Before the fix, every removal (stale entry or a freshly expired
// one) went through removePendingAuthorization, which reads, filters and writes
// back the WHOLE index on its own -- O(n) index rewrites for n authorizations
// expiring together in one sweep, each one re-reading and re-filtering an index of
// up to n entries, for O(n^2) total work. This funds and expires a batch together
// and checks the index was rewritten at most once for the whole sweep.
func TestSweepExpiredRewritesThePendingIndexAtMostOnce(t *testing.T) {
	const n = 40
	backing := newMemoryLifecycleState()
	counting := &pendingIndexWriteCountingState{memoryLifecycleState: backing}
	f := newSweepFixtureOn(t, counting, backing)

	ids := make([][32]byte, 0, n)
	for i := 0; i < n; i++ {
		_, id := f.hold(byte(i+1), 10)
		ids = append(ids, id)
	}
	counting.writes = 0 // only count what SweepExpired itself does from here

	voided, err := f.engine.SweepExpired(f.sweepAt())
	if err != nil {
		t.Fatalf("sweep error: %v", err)
	}
	if len(voided) != n {
		t.Fatalf("sweep voided %d authorizations, want %d", len(voided), n)
	}
	if counting.writes > 1 {
		t.Fatalf("sweep wrote the pending index %d times for %d authorizations expiring together, want at most 1 (one rewrite per removal is the PL-EXT-26 bug)", counting.writes, n)
	}
	f.requirePending()
}
