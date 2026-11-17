package bft

// The round a height starts in.
//
// Two validators stay in step only if each starts a height in the round the other starts
// it in. A validator sends nothing in a round in which it neither proposes nor has a
// proposal to vote on, so two validators a round apart hear nothing from each other until
// the round of the one behind times out (or a round comes whose proposer is the one ahead),
// which takes a whole commit timeout. Every height starts in round 1 -- commit leaves
// round 0, and startNewRound moves on one round from it, as it does for an engine that has
// just been built -- and two things used to make a validator start one in another round:
//
//   - a block the node took from its peer (rather than one this validator committed itself)
//     moved the chain on inside syncHeightWithNodeLocked, which sets round 0 and reports the
//     move, so that startNewRound did not move on from it: the height that followed a block
//     learned from the peer started in round 0, at the validator that learned it, and in
//     round 1 at the one that committed it;
//   - the node answers a second commit of a block it already holds with no error, and the
//     peer's block reaches it while the engine is committing the same one, so the sync path
//     signals (NotifyExternalCommit) a block the engine has committed itself. The signal
//     was taken for a round that had to be given up and ended the first round of the next
//     height before anything was done in it, and the height started in round 2 at that
//     validator and in round 1 at the other.
//
// Round 1, and not 0, because that is where a validator that has not been replaced yet starts
// the height after its own commit: a pair of which one runs the older engine must agree too.
//
// Each test below fails on the engine that has either flaw.

import (
	"fmt"
	"testing"
	"time"
)

// endRound ends the round the engine is in, the way a later round that the others have gone
// on to does. The tests that run a round loop use it to stop the loop: a NotifyExternalCommit
// signal ends a round only when the chain has reached its height (chainReached).
func endRound(e *Engine) {
	select {
	case e.roundSkipCh <- struct{}{}:
	default:
	}
}

// Validator 0 commits height 1 through its own round and its peer, validator 1, takes the
// block from it (the way a block sent by a peer reaches a node): both must start height 2 in
// the same round, and in round 1. A validator restarted at that point starts it there too,
// whether its node was already at height 1 when it was built or caught up while it waited to
// start (Start waits for its peers).
func TestTwoValidatorsStartTheNextHeightInTheSameRoundWhicheverOfThemCommittedTheBlock(t *testing.T) {
	const want = 1
	tv := newTestValidators(t, 2)
	newEngineOf := func(idx int) (*Engine, *signGuardNode) {
		node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[idx]}
		return NewEngine(node, tv.keys[idx], &captureBroadcaster{}), node
	}

	// Height 1 is decided in round 1 and committed by validator 0.
	a, _ := newEngineOf(0)
	inRound(a, 1, 1)
	decideHeight(t, tv, a, 1, 1)
	a.mu.RLock()
	block := a.activeProposal.Proposal.Block
	a.mu.RUnlock()
	if !a.commit() {
		t.Fatalf("test setup: validator 0 did not commit height 1")
	}
	a.startNewRound()

	// Validator 1 was in the same round when its node took the block.
	b, nodeB := newEngineOf(1)
	inRound(b, 1, 1)
	nodeB.applySynced(block)
	b.NotifyExternalCommit()
	b.startNewRound()

	if roundA, roundB := roundOf(a), roundOf(b); roundA != roundB || roundA != want {
		t.Fatalf("height 2 started in round %d at the validator that committed height 1 and in round %d at the one that took the block from it, want round %d at both", roundA, roundB, want)
	}

	t.Run("restarted at the tip", func(t *testing.T) {
		node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[1]}
		node.applySynced(block)
		restarted := NewEngine(node, tv.keys[1], &captureBroadcaster{})
		restarted.startNewRound()
		if got := roundOf(restarted); got != want {
			t.Fatalf("a validator restarted at height 2 started it in round %d, want round %d", got, want)
		}
	})
	t.Run("restarted, and the node caught up while it waited to start", func(t *testing.T) {
		restarted, node := newEngineOf(1) // built with the node at height 0
		node.applySynced(block)
		restarted.startNewRound()
		if got := roundOf(restarted); got != want {
			t.Fatalf("a validator whose node caught up with its peer before its first round started height 2 in round %d, want round %d", got, want)
		}
	})
}

// A signal for a block this engine has committed itself -- the peer's copy of it reached the
// node while the engine was committing it, and the sync path tells the engine about it -- is
// not news, and does not end the first round of the next height, in the wait for the minimum
// block interval or in the round itself.
func TestASignalForABlockTheEngineCommittedItselfDoesNotEndTheFirstRoundOfTheNextHeight(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval time.Duration
	}{
		{"while the round waits out the minimum block interval", 400 * time.Millisecond},
		{"in the round", 0},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tv := newTestValidators(t, 2)
			node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}
			engine := NewEngine(node, tv.keys[0], &captureBroadcaster{}, WithTimeouts(intervalTimers), WithMinBlockInterval(tc.interval))
			inRound(engine, 1, 1)
			decideHeight(t, tv, engine, 1, 1)
			if !engine.commit() {
				t.Fatalf("test setup: height 1 did not commit")
			}
			engine.NotifyExternalCommit() // the sync path, told about the block the node already holds

			done := make(chan struct{})
			go func() {
				defer close(done)
				engine.runRound()
			}()
			defer func() {
				endRound(engine)
				<-done
			}()
			for started := time.Now(); time.Since(started) < 2*time.Second; time.Sleep(time.Millisecond) {
				if height, round, _ := engine.Status(); height == 2 && round == 1 {
					break
				}
			}
			select {
			case <-done:
				t.Fatalf("the first round of height 2 was ended by a signal for the block of height 1, which this engine had committed itself; it went on to round %d", roundOf(engine))
			case <-time.After(150 * time.Millisecond):
			}
			if height, round, _ := engine.Status(); height != 2 || round != 1 {
				t.Fatalf("the engine is in round %d of height %d, want round 1 of height 2", round, height)
			}
		})
	}
}

// What the check above must not do: a signal for a block that the chain does hold at the
// height of the round still ends it, in the wait for the interval and in the round.
func TestASignalForABlockTheChainHasReachedStillEndsTheRound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval time.Duration
	}{
		{"while the round waits out the minimum block interval", 5 * time.Second},
		{"in the round", 0},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tv := newTestValidators(t, 2)
			node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}
			engine := NewEngine(node, tv.keys[0], &captureBroadcaster{}, WithTimeouts(intervalTimers), WithMinBlockInterval(tc.interval))
			engine.noteCommitSeen() // the wait, when there is an interval, has all of it left to run
			done := make(chan struct{})
			go func() {
				defer close(done)
				engine.runRound()
			}()
			time.Sleep(50 * time.Millisecond)

			node.applySynced(candidateBlock(1, 1, tv.addrs[1], 0x21)) // the node takes the block of height 1 from the peer
			engine.NotifyExternalCommit()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				endRound(engine)
				<-done
				t.Fatalf("the round of height 1 was not ended by a signal for a block the chain holds at that height")
			}
		})
	}
}

// startsOf lists, for the measured heights of a run that the two validators started in
// different rounds, the height and the two rounds.
func startsOf(run cadenceRun) string {
	out := ""
	for _, h := range run.heightsOf {
		a, seenA := run.starts[0][h]
		b, seenB := run.starts[1][h]
		if seenA && seenB && a != b {
			out += fmt.Sprintf(" %d:%d/%d", h, a, b)
		}
	}
	return out
}

// requireInStep fails when the run has a height that the two validators started in
// different rounds, that took a failed round to decide, or that was decided in a round
// after the first.
func requireInStep(t *testing.T, run cadenceRun, heights int) {
	t.Helper()
	if len(run.intervals) < heights {
		t.Fatalf("LIVENESS: %d heights in the time allowed, want %d\n%s", len(run.intervals), heights, run)
	}
	differ, both := run.startMismatch()
	if both < heights/2 {
		t.Fatalf("test setup: the start of only %d heights was seen at both validators\n%s", both, run)
	}
	if differ != 0 || run.stalled() != 0 || run.laterRound() != 0 {
		t.Fatalf("the validators did not start every height in the same round: %d of %d heights started in different rounds (%s), %d stalled, %d were decided after round 1\n%s",
			differ, both, startsOf(run), run.stalled(), run.laterRound(), run)
	}
}

// A validator that never commits a height through its own round -- the precommits of its
// peer never reach it, so it takes every block from the peer, as the node does when the
// block outruns the vote -- starts every height in the round its peer does.
func TestAValidatorThatAlwaysTakesTheBlockFromItsPeerStartsEveryHeightInStepWithIt(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	const scale, heights = 10, 24
	m := liveCadenceModel(scale)
	m.minInterval = time.Second / scale
	m.syncOnly[1] = true
	m.sampleStarts = true
	run := runCadence(t, m, heights+cadenceWarmup, 2*time.Minute)
	if run.viaSync[1] < heights {
		t.Fatalf("test setup: validator 1 took %d blocks from its peer, want every one of %d\n%s", run.viaSync[1], heights, run)
	}
	requireInStep(t, run, heights)
	t.Logf("%s", run)
}

// Two validators that both commit every height through their own rounds start every height
// in the same round, though the block of each reaches the other's node while it commits the
// same one (the slower validator, which is the one that sees it, has its own commit still
// running when the peer's copy arrives) and the node answers that without an error. The pair
// is also started with one validator a round behind the other.
func TestTwoValidatorsThatCommitTheSameBlockTwiceStartEveryHeightInStep(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	const scale, heights = 10, 24
	for _, tc := range []struct {
		name      string
		oneBehind bool
	}{
		{"started in step", false},
		{"started one round behind", true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m := liveCadenceModel(scale)
			m.minInterval = time.Second / scale
			m.oneBehind = tc.oneBehind
			m.sampleStarts = true
			run := runCadence(t, m, heights+cadenceWarmup, 2*time.Minute)
			requireInStep(t, run, heights)
			t.Logf("%s", run)
		})
	}
}
