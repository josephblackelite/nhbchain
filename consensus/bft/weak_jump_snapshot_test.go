package bft

// FINDING B regression tests.
//
// admitWeakRoundJumpLocked (round_sync.go) bounds the last, no-floor rule of
// supportedRoundLocked to at most maxFutureRounds distinct rounds per height.
// Before weak_jump_snapshot.go existed, that bound -- weakJumpHeight and
// weakJumpRounds on Engine -- was plain, non-persisted memory: restarting the
// very validator it protects reset it to zero while the height itself had not
// changed, so a lone validator under a third of the power could win a fresh
// maxFutureRounds weak jumps on every restart of its victim, unboundedly -- the
// identical unbounded-drag liveness hole this whole feature exists to close,
// gated by restart count instead of by message count.
//
// These tests exercise the real production functions (writeWeakJumpSnapshot,
// readWeakJumpSnapshot, NewEngine's restore step via restoreWeakJumpState,
// admitWeakRoundJumpLocked's write) directly, the same way lock_snapshot_test.go
// and sign_state_test.go do for their own durable records.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWeakJumpSnapshotRoundTrip proves a written snapshot reads back exactly,
// and that repeated writes of identical content (no new jump granted) are
// idempotent -- the file's content after N writes of the same value is
// byte-for-byte the same as after 1.
func TestWeakJumpSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bft_weak_jump.json")
	snap := weakJumpSnapshot{Height: 7, Rounds: []int{2, 3, 5}}

	for i := 0; i < 3; i++ {
		if err := writeWeakJumpSnapshot(path, snap); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	got, err := readWeakJumpSnapshot(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got == nil || got.Height != snap.Height || len(got.Rounds) != len(snap.Rounds) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, snap)
	}
	for i, r := range snap.Rounds {
		if got.Rounds[i] != r {
			t.Fatalf("round-trip mismatch at index %d: got %v, want %v", i, got.Rounds, snap.Rounds)
		}
	}

	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file directly: %v", err)
	}
	if err := writeWeakJumpSnapshot(path, snap); err != nil {
		t.Fatalf("re-write: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file directly (2nd): %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("expected repeated writes of identical content to be idempotent byte-for-byte")
	}
}

// TestReadWeakJumpSnapshotMissingOrCorruptIsNeverFatal proves a missing file and
// a corrupt/truncated file both come back as "no snapshot" (nil, nil), and that a
// blank path is a no-op for both read and write -- startup must never fail, and
// must never half-apply a partial bound, just because the file is absent,
// unreadable, or persistence was never turned on.
func TestReadWeakJumpSnapshotMissingOrCorruptIsNeverFatal(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "does_not_exist.json")
	snap, err := readWeakJumpSnapshot(missing)
	if err != nil || snap != nil {
		t.Fatalf("missing file: expected (nil, nil), got (%+v, %v)", snap, err)
	}

	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	snap, err = readWeakJumpSnapshot(corrupt)
	if err != nil || snap != nil {
		t.Fatalf("corrupt file: expected (nil, nil), got (%+v, %v)", snap, err)
	}

	if err := writeWeakJumpSnapshot("", weakJumpSnapshot{Height: 1, Rounds: []int{1}}); err != nil {
		t.Fatalf("blank path write should be a no-op, got: %v", err)
	}
	snap, err = readWeakJumpSnapshot("")
	if err != nil || snap != nil {
		t.Fatalf("blank path read: expected (nil, nil), got (%+v, %v)", snap, err)
	}
}

// TestARestartOfTheDraggedValidatorDoesNotResetTheWeakJumpBudget is the FINDING
// B regression test. Validator 1 holds a quarter of the power and, exactly as in
// TestALoneValidatorCannotDragThisEngineThroughUnboundedRoundsOfRealMessages,
// claims one round further each time this engine's own round catches up to it,
// with validator 2 kept level so the shortfall the no-floor rule acts on never
// closes on its own. The engine is driven through the full maxFutureRounds
// budget, then rebuilt -- a fresh Engine at the same height, built the ordinary
// way (engineAt, which is NewEngine underneath), against the SAME weak-jump
// snapshot path -- simulating a restart of the dragged validator. The claimant
// must not be able to win a single additional weak jump at this height across
// the restart: the two incarnations share one height's budget, not one each.
func TestARestartOfTheDraggedValidatorDoesNotResetTheWeakJumpBudget(t *testing.T) {
	tv := newWeightedValidators(t, 1, 1, 1, 1)
	path := filepath.Join(t.TempDir(), "bft_weak_jump.json")

	engine, _, _ := engineAt(t, tv, 1, WithWeakJumpSnapshotPath(path))
	for i := 1; i <= maxFutureRounds; i++ {
		sendVote(t, engine, tv, 2, roundOf(engine)) // the level peer: always freshly at this validator's own round
		sendVote(t, engine, tv, 1, 1+i*1000)        // the lone claimant: ever further ahead
		engine.startNewRound()
	}
	before := roundOf(engine)
	if want := 1 + maxFutureRounds*1000; before != want {
		t.Fatalf("setup: validator 0 is in round %d after %d granted jumps, want %d", before, maxFutureRounds, want)
	}
	engine.mu.RLock()
	spentBefore := len(engine.weakJumpRounds)
	engine.mu.RUnlock()
	if spentBefore != maxFutureRounds {
		t.Fatalf("setup: %d of the weak jump budget spent, want the full %d before simulating a restart", spentBefore, maxFutureRounds)
	}

	// Simulate a restart: a brand-new Engine, at the same height as the one
	// above (engineAt always starts at height 1), built the ordinary way, with
	// the same durable path -- exactly what a real process restart looks like.
	restarted, _, _ := engineAt(t, tv, before, WithWeakJumpSnapshotPath(path))
	restarted.mu.RLock()
	recovered := len(restarted.weakJumpRounds)
	restarted.mu.RUnlock()
	if recovered != maxFutureRounds {
		t.Fatalf("the restarted engine recovered %d of the %d rounds already granted at this height, want all %d: the budget must survive the restart it is meant to survive", recovered, maxFutureRounds, maxFutureRounds)
	}

	// No further weak jump is available to the same claimant after the restart.
	claimed := before + 1000
	sendVote(t, restarted, tv, 2, roundOf(restarted))
	sendVote(t, restarted, tv, 1, claimed)
	restarted.startNewRound()

	restarted.mu.RLock()
	spentAfter := len(restarted.weakJumpRounds)
	restarted.mu.RUnlock()
	if spentAfter != maxFutureRounds {
		t.Fatalf("the restarted engine's weak jump bound now holds %d rounds, want it to stay at %d: a 9th grant was allowed after the restart", spentAfter, maxFutureRounds)
	}
	if got := roundOf(restarted); got == claimed {
		t.Fatalf("the restarted engine followed the claimant to round %d, a 9th weak jump: a restart must not hand the same claimant a fresh budget", got)
	}
}
