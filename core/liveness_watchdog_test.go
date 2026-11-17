package core

// The watchdog exists to speak when the consensus engine cannot. The engine holds
// its own lock for as long as the node takes to commit a block, so a stall report
// that waited for the engine's status said nothing in exactly the case it was
// written for: the single watchdog goroutine sat in the report, no stall line was
// logged, and the seconds-since-commit gauge stopped moving. These tests replace
// the engine with one whose lock is held until the test lets it go.

import (
	"log/slog"
	"sync"
	"testing"
	"time"
)

// heldBFTStatus is a consensus engine whose lock is held: it answers a status
// request only once released, and gives up at the end of the wait it is given, as
// the real engine's StatusWithin does.
type heldBFTStatus struct {
	free chan struct{}
	once sync.Once
}

func newHeldBFTStatus() *heldBFTStatus { return &heldBFTStatus{free: make(chan struct{})} }

func (h *heldBFTStatus) release() { h.once.Do(func() { close(h.free) }) }

func (h *heldBFTStatus) StatusWithin(wait time.Duration) (uint64, int, int, bool) {
	select {
	case <-h.free:
		return 41, 2, 1, true
	case <-time.After(wait):
		return 0, 0, 0, false
	}
}

// within runs f and fails the test, releasing the engine so nothing is left
// waiting, if f has not returned after limit.
func within(t *testing.T, limit time.Duration, held *heldBFTStatus, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		held.release()
		<-done
		t.Fatalf("%s was still waiting for the consensus engine after %v", what, limit)
	}
}

func TestWatchdogReportsAStallAndTheRecoveryWhileTheEngineIsHeldByACommit(t *testing.T) {
	logs := captureLogs(t)
	node := newTestNode(t)
	node.setBuildConfig(func(cfg *buildConfig) { cfg.StallAfter = 60 * time.Second })
	held := newHeldBFTStatus()
	t.Cleanup(held.release)
	node.build.bftStatus = held
	start := time.Unix(1_800_000_000, 0)

	node.livenessCheck(start) // the first observation: process start counts as a commit

	within(t, 10*time.Second, held, "the watchdog tick", func() { node.livenessCheck(start.Add(61 * time.Second)) })
	stall := logs.find("LIVENESS: no block committed for 61s")
	if len(stall) != 1 {
		t.Fatalf("expected the stall line while the engine was held, got %d", len(stall))
	}
	if v, ok := attr(stall[0], "bft_engine_busy"); !ok || !v.Bool() {
		t.Fatalf("the stall line must say the engine was busy, got %v (present %v)", v, ok)
	}
	if v, ok := attr(stall[0], "event"); !ok || v.String() != "no_commit" {
		t.Fatalf("event = %v", v)
	}
	if v := livenessMetricValue(t, "nhb_consensus_liveness_stalled", nil); v != 1 {
		t.Fatalf("stalled gauge = %v during a stall", v)
	}
	if v := livenessMetricValue(t, "nhb_consensus_seconds_since_last_commit", nil); v != 61 {
		t.Fatalf("seconds since last commit = %v, want 61", v)
	}

	// The commit finishes, the chain moves, and the next tick reports it with the
	// engine's own numbers.
	held.release()
	livenessMine(t, node)
	within(t, 10*time.Second, held, "the watchdog tick", func() { node.livenessCheck(start.Add(130 * time.Second)) })
	recovered := logs.find("LIVENESS: recovered")
	if len(recovered) != 1 {
		t.Fatalf("expected one recovery line, got %d", len(recovered))
	}
	if v, ok := attr(recovered[0], "bft_engine_busy"); !ok || v.Bool() {
		t.Fatalf("the recovery line must not say the engine was busy, got %v (present %v)", v, ok)
	}
	if v, _ := attr(recovered[0], "bft_height"); v.Uint64() != 41 {
		t.Fatalf("bft_height = %d, want the engine's 41", v.Uint64())
	}
	if v, _ := attr(recovered[0], "bft_round"); v.Int64() != 2 {
		t.Fatalf("bft_round = %d, want the engine's 2", v.Int64())
	}
	if v, _ := attr(recovered[0], "bft_locked_round"); v.Int64() != 1 {
		t.Fatalf("bft_locked_round = %d, want the engine's 1", v.Int64())
	}
}

func TestLivenessSnapshotDoesNotWaitOnABusyEngine(t *testing.T) {
	node := newTestNode(t)
	held := newHeldBFTStatus()
	t.Cleanup(held.release)
	node.build.bftStatus = held

	var snap LivenessSnapshot
	within(t, 10*time.Second, held, "the liveness snapshot", func() { snap = node.LivenessSnapshot() })
	if !snap.BFTBusy {
		t.Fatalf("the snapshot must say the engine was busy: %+v", snap)
	}
	if snap.BFTHeight != 0 || snap.BFTRound != 0 || snap.BFTLockedRound != 0 {
		t.Fatalf("a busy engine reports no numbers, got height %d round %d locked %d", snap.BFTHeight, snap.BFTRound, snap.BFTLockedRound)
	}
	// The report's other fields do not depend on the engine.
	if snap.Height != node.chain.GetHeight() {
		t.Fatalf("snapshot height = %d, want the chain's %d", snap.Height, node.chain.GetHeight())
	}

	// Every LIVENESS line is built from the snapshot, so all of them carry the flag.
	var fields []any
	within(t, 10*time.Second, held, "building the fields of a LIVENESS line", func() { fields = node.livenessFields("empty_block_fallback") })
	flagged := false
	for _, f := range fields {
		if a, ok := f.(slog.Attr); ok && a.Key == "bft_engine_busy" {
			flagged = a.Value.Bool()
		}
	}
	if !flagged {
		t.Fatalf("the fields of a LIVENESS line must carry bft_engine_busy=true while the engine is held")
	}

	held.release()
	snap = node.LivenessSnapshot()
	if snap.BFTBusy || snap.BFTHeight != 41 || snap.BFTRound != 2 || snap.BFTLockedRound != 1 {
		t.Fatalf("once the engine answers the snapshot carries its numbers: %+v", snap)
	}
}
