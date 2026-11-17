package bft

// The minimum block interval (WithMinBlockInterval).
//
// Nothing in the engine spaces one block from the next: a round for the next height
// starts the moment the last one commits, so on an idle chain the pace of blocks is the
// speed of the messages and of the commit (a few tens of blocks a second between two
// validators in step) less whatever the pair loses to rounds that fail (see
// cadence_test.go, which measures it). An engine that loses fewer rounds therefore
// produces blocks several times faster, and every quantity of the chain that is
// counted in blocks -- epoch length, emission per epoch, interest per block -- runs
// several times faster with it. The interval is the explicit, configurable pace: after
// a validator sees a block commit, it lets the interval pass before it starts the round
// of the next height.
//
// Each test below fails on the engine without it.

import (
	"testing"
	"time"

	"nhbchain/p2p"
)

// intervalTimers are round timers long enough that nothing in a test below ends a
// round by itself.
var intervalTimers = TimeoutConfig{Proposal: 10 * time.Second, Prevote: 10 * time.Second, Precommit: 10 * time.Second, Commit: 20 * time.Second}

// firstProposalAfter waits for the first proposal broadcast on br and returns when it
// was seen, or the zero time when none comes within the limit.
func firstProposalAfter(br *captureBroadcaster, limit time.Duration) time.Time {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		for _, msg := range br.messages() {
			if msg.Type == p2p.MsgTypeProposal {
				return time.Now()
			}
		}
		time.Sleep(time.Millisecond)
	}
	return time.Time{}
}

// The round that follows a commit does not propose before the interval has passed. A
// validator that is the proposer of the next height commits height 1 and starts its next
// round at once: with an interval of 250 ms its proposal is not on the wire before 250 ms
// after the commit; with none it is there within a fraction of that.
func TestTheNextRoundDoesNotProposeBeforeTheMinimumBlockIntervalHasPassed(t *testing.T) {
	const interval = 250 * time.Millisecond
	tv := newTestValidators(t, 2)
	// Validator 1 proposes round 1 of height 1; validator 0, the one under test, round 1 of height 2.
	tips := tipsForProposers(t, tv, 1, 1, 0)

	for _, tc := range []struct {
		name string
		opts []Option
		want time.Duration // the shortest time from the commit to the proposal
	}{
		{"with an interval", []Option{WithMinBlockInterval(interval)}, interval},
		{"without one", nil, 0},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}
			br := &captureBroadcaster{}
			engine := NewEngine(chainTipNode{node, tips}, tv.keys[0], br, append([]Option{WithTimeouts(intervalTimers)}, tc.opts...)...)
			inRound(engine, 1, 1)
			decideHeight(t, tv, engine, 1, 1)

			committing := time.Now()
			if !engine.commit() {
				t.Fatalf("test setup: height 1 did not commit")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				engine.runRound()
			}()
			defer func() {
				endRound(engine)
				<-done
			}()

			proposed := firstProposalAfter(br, 5*time.Second)
			if proposed.IsZero() {
				t.Fatalf("no proposal for height 2 within 5 s of the commit")
			}
			took := proposed.Sub(committing)
			if took < tc.want {
				t.Fatalf("the proposal for the next height was on the wire %v after the commit, before the %v interval had passed", took, tc.want)
			}
			if tc.want == 0 && took >= interval {
				t.Fatalf("without an interval the proposal took %v: the round should start at once (the test could not tell an interval from a slow machine)", took)
			}
		})
	}
}

// The interval is only what is left of it since the last commit, and only after a
// commit: an engine that has committed nothing (a validator that has just started, or
// one whose last round failed and lasted longer than the interval) starts its round
// without waiting.
func TestTheMinimumBlockIntervalIsWaitedOutOnceAndOnlyAfterACommit(t *testing.T) {
	const interval = 200 * time.Millisecond
	tv := newTestValidators(t, 2)
	newEngine := func() *Engine {
		engine, _, _ := newVoter(t, tv, WithTimeouts(intervalTimers), WithMinBlockInterval(interval))
		return engine
	}
	waitTook := func(e *Engine) (time.Duration, bool) {
		start := time.Now()
		interrupted := e.waitMinBlockInterval()
		return time.Since(start), interrupted
	}

	t.Run("no commit yet", func(t *testing.T) {
		if took, interrupted := waitTook(newEngine()); took > interval/2 || interrupted {
			t.Fatalf("an engine that has seen no commit waited %v (interrupted %v)", took, interrupted)
		}
	})
	t.Run("just committed", func(t *testing.T) {
		engine := newEngine()
		armed := time.Now()
		engine.noteCommitSeen()
		took, interrupted := waitTook(engine)
		if interrupted {
			t.Fatalf("the wait was interrupted with nothing to interrupt it")
		}
		if total := time.Since(armed); total < interval {
			t.Fatalf("the round could start %v after the commit, before the %v interval", total, interval)
		}
		if took > 3*interval {
			t.Fatalf("the wait took %v, want about the interval, %v", took, interval)
		}
	})
	t.Run("part of the interval has passed", func(t *testing.T) {
		engine := newEngine()
		armed := time.Now()
		engine.noteCommitSeen()
		time.Sleep(interval / 2)
		took, _ := waitTook(engine)
		if total := time.Since(armed); total < interval {
			t.Fatalf("the round could start %v after the commit, before the %v interval", total, interval)
		}
		if took > interval*9/10 {
			t.Fatalf("waited %v after half the interval had passed: it should wait only for the rest (about %v)", took, interval/2)
		}
	})
	t.Run("all of it has passed", func(t *testing.T) {
		engine := newEngine()
		engine.noteCommitSeen()
		time.Sleep(interval + 20*time.Millisecond)
		if took, interrupted := waitTook(engine); took > interval/2 || interrupted {
			t.Fatalf("waited %v (interrupted %v) after the interval had passed: a round that follows a failed one must not wait", took, interrupted)
		}
	})
}

// A round that is over taken does not go on waiting: a block committed elsewhere, or a
// later round the others are in, ends the wait and the round loop starts another round
// (which is what ends a round in progress on the same two events). A block committed
// elsewhere is one the chain holds by the height of the round: a signal for one that the
// engine has committed itself does not end it (round_start_test.go).
func TestTheMinimumBlockIntervalWaitIsGivenUpWhenTheRoundIsOvertaken(t *testing.T) {
	tv := newTestValidators(t, 2)
	// The interval is long, so that a wait that ends early can only have been given up.
	const interval = 2 * time.Second
	for _, tc := range []struct {
		name   string
		signal func(*Engine, *signGuardNode)
	}{
		{"a block committed outside the round", func(e *Engine, n *signGuardNode) {
			n.applySynced(candidateBlock(1, 1, tv.addrs[1], 0x31))
			e.NotifyExternalCommit()
		}},
		{"a later round the others are in", func(e *Engine, _ *signGuardNode) { e.roundSkipCh <- struct{}{} }},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			engine, node, _ := newVoter(t, tv, WithTimeouts(intervalTimers), WithMinBlockInterval(interval))
			engine.noteCommitSeen()
			result := make(chan bool, 1)
			started := time.Now()
			go func() { result <- engine.waitMinBlockInterval() }()
			time.Sleep(20 * time.Millisecond)
			tc.signal(engine, node)
			select {
			case interrupted := <-result:
				if !interrupted {
					t.Fatalf("the wait ended without saying it was given up")
				}
				if took := time.Since(started); took > interval/2 {
					t.Fatalf("the wait went on for %v after the signal", took)
				}
			case <-time.After(interval):
				t.Fatalf("the wait was not given up when the round was overtaken")
			}
		})
	}
}

// A block committed outside the engine's own rounds (a synced block) counts as a commit
// for the interval, so that a validator which was brought up to date by its peer does not
// start proposing on top of it earlier than one that committed the block itself.
func TestABlockCommittedOutsideTheEngineStartsTheMinimumBlockInterval(t *testing.T) {
	const interval = 200 * time.Millisecond
	tv := newTestValidators(t, 2)
	engine, _, _ := newVoter(t, tv, WithTimeouts(intervalTimers), WithMinBlockInterval(interval))
	armed := time.Now()
	engine.NotifyExternalCommit()
	<-engine.externalCommitCh // the signal itself is not what is being tested
	engine.waitMinBlockInterval()
	if total := time.Since(armed); total < interval {
		t.Fatalf("the round could start %v after a synced block, before the %v interval", total, interval)
	}
}

// The interval is a local pace, not something a peer can be made to wait for, and not
// something a mistake in a config can make long enough to stop the chain: it is never
// more than half the commit timeout, and never negative.
func TestTheMinimumBlockIntervalIsBounded(t *testing.T) {
	tv := newTestValidators(t, 2)
	for _, tc := range []struct {
		name string
		opts []Option
		want time.Duration
	}{
		{"unset", nil, 0},
		{"zero", []Option{WithMinBlockInterval(0)}, 0},
		{"negative", []Option{WithMinBlockInterval(-time.Second)}, 0},
		{"one second", []Option{WithMinBlockInterval(time.Second)}, time.Second},
		{"half the default commit timeout", []Option{WithMinBlockInterval(2 * time.Second)}, 2 * time.Second},
		{"longer than that", []Option{WithMinBlockInterval(time.Hour)}, 2 * time.Second},
		{"longer than half of a shorter commit timeout", []Option{WithTimeouts(TimeoutConfig{Commit: time.Second}), WithMinBlockInterval(time.Minute)}, 500 * time.Millisecond},
		{"set before the timeout it depends on", []Option{WithMinBlockInterval(time.Minute), WithTimeouts(TimeoutConfig{Commit: time.Second})}, 500 * time.Millisecond},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			engine, _, _ := newVoter(t, tv, tc.opts...)
			if got := engine.MinBlockInterval(); got != tc.want {
				t.Fatalf("minimum block interval %v, want %v", got, tc.want)
			}
		})
	}
}

// Two real engines with an interval never commit a block sooner after the last one than
// the interval -- whichever path each block came by -- and an idle pair without one is
// several times faster, so the interval is what sets the pace. The pair is the rig of
// cadence_test.go with no noise: messages that cross in 1 ms, the costs of a build, a
// validation and a commit as they are live, and round timers a tenth of the default.
func TestTwoValidatorsCommitNoFasterThanTheMinimumBlockInterval(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	const scale = 10
	// 1 s live: the default of the config, at the scale of the timers of the run.
	interval := time.Second / scale
	const heights = 25

	pace := func(min time.Duration) cadenceRun {
		m := liveCadenceModel(scale)
		m.minInterval = min
		return runCadence(t, m, heights+cadenceWarmup, 2*time.Minute)
	}
	with, without := pace(interval), pace(0)

	if len(with.intervals) < heights || len(without.intervals) < heights {
		t.Fatalf("LIVENESS: %d heights with an interval and %d without, of %d", len(with.intervals), len(without.intervals), heights)
	}
	// One validator sees its own commit a little before or after the log line that
	// stamps it, so the spacing is judged with a little slack; it is still an order
	// of magnitude above what the pair does without an interval.
	const slack = 5 * time.Millisecond
	for i, d := range with.intervals {
		if d < interval-slack {
			t.Fatalf("height %d was committed %v after the one before it, less than the %v interval\nwith an interval: %s\nwithout one:      %s", i+cadenceWarmup+1, d, interval, with, without)
		}
	}
	if fast, paced := without.quantile(.5), with.quantile(.5); paced < 2*fast {
		t.Fatalf("an interval of %v made no difference to the pace: median height %v with it, %v without\nwith an interval: %s\nwithout one:      %s", interval, paced, fast, with, without)
	}
	t.Logf("idle pair, %d heights: without an interval %s", heights, without)
	t.Logf("idle pair, %d heights: with a %v interval %s", heights, interval, with)
}

// withE2EMinInterval gives the engines of the restart tests of round_restart_test.go a
// minimum block interval, and starts the running validator as if it had just committed.
func withE2EMinInterval(t *testing.T, interval time.Duration) {
	t.Helper()
	before := e2eMinInterval
	e2eMinInterval = interval
	t.Cleanup(func() { e2eMinInterval = before })
}

// A restart is not slowed by the interval: the validator that was away starts its rounds
// without waiting (it has seen no commit), and the one that kept running waits only what is
// left of the interval since its last commit -- nothing, after rounds that failed. So the
// restart tests run again below with the longest interval the engine allows on both
// (half the commit timer, 20 ms at these timers, 2 s at the default ones) and the running
// validator having just committed: the first round of the one that kept running starts
// only after the interval, while the other, restarted, is already listening.
const e2eInterval = 20 * time.Millisecond

func TestARestartedValidatorRejoinsARunningPeerAtAnyRoundWithAMinimumBlockInterval(t *testing.T) {
	withE2EMinInterval(t, e2eInterval)
	TestARestartedValidatorRejoinsARunningPeerAtAnyRound(t)
}

func TestALockedValidatorAndARestartedPeerCommitWithinFewRoundsWithAMinimumBlockInterval(t *testing.T) {
	withE2EMinInterval(t, e2eInterval)
	TestALockedValidatorAndARestartedPeerCommitWithinFewRounds(t)
}

func TestALockedValidatorRefusesAtMostOneBlockBeforeThePeerReProposesItsOwnWithAMinimumBlockInterval(t *testing.T) {
	withE2EMinInterval(t, e2eInterval)
	TestALockedValidatorRefusesAtMostOneBlockBeforeThePeerReProposesItsOwn(t)
}
