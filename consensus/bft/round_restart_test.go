package bft

// Restart regression tests: two real engines over real messages and real (scaled)
// timers, one of them restarted while the other keeps running.
//
// The stall these reproduce: with two validators, one is restarted while the other
// keeps running. The one that ran went up a round about every 4 seconds with nobody
// to make a quorum with; the one that came back waited 30 seconds before it started
// its rounds -- meanwhile buffering every message of the rounds it missed -- and
// then walked through those rounds one round timeout each, always behind the round
// its peer was in, so that each waited for votes in rounds the other was not in.
// Block production stopped for over a minute. In one episode the validator that ran
// had locked on a block at round 7, refused every later proposal of a validator
// that knew nothing of the lock, and committed only when its own proposer turn came.
//
// The tests scale the timers down by a factor of 100 (the default round timeouts
// are 2 s, 2 s, 2 s and 4 s: proposal, prevote, precommit, commit; here 20 ms,
// 20 ms, 20 ms and 40 ms) and the restarted validator's 30 second wait to 8 rounds,
// so a round of these tests stands for a round of a node with the default timeouts.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"nhbchain/core/types"
)

var e2eTimeouts = TimeoutConfig{
	Proposal:  20 * time.Millisecond,
	Prevote:   20 * time.Millisecond,
	Precommit: 20 * time.Millisecond,
	Commit:    40 * time.Millisecond,
}

// e2eMinInterval, when a test sets it (withE2EMinInterval), is the minimum block interval
// of the engines of these tests, and the running validator starts as if it had just
// committed a block, so that its first round has the interval to wait out.
var e2eMinInterval time.Duration

// e2eOptions are the options of the engines of these tests that run rounds.
func e2eOptions(extra ...Option) []Option {
	return append([]Option{WithTimeouts(e2eTimeouts), WithMinBlockInterval(e2eMinInterval)}, extra...)
}

const (
	// e2eRound is how long a round that does not commit lasts in these tests.
	e2eRound = 40 * time.Millisecond
	// e2eBootRounds is how many rounds the restarted validator waits, listening,
	// before it starts its own: the 30 seconds the engine waits for its peers
	// (Engine.Start) is 7.5 rounds of 4 seconds.
	e2eBootRounds = 8
)

// shapedNode makes the schedule of proposers of a test chain what the test needs.
// stake, when set, is what the proposer of a round is chosen by, instead of the
// voting power: the voting power (what makes a quorum) stays equal, so both
// validators are needed for every quorum, while the schedule can be made to
// favour one of them. lastCommit is the hash the choice is seeded with (the last
// block's), which is all that differs between two chains of the same validators.
type shapedNode struct {
	*signGuardNode
	stake      map[string]int64
	lastCommit []byte
}

func (n shapedNode) GetAccount(addr []byte) (*types.Account, error) {
	if n.stake == nil {
		return n.signGuardNode.GetAccount(addr)
	}
	return &types.Account{Stake: big.NewInt(n.stake[string(addr)])}, nil
}

func (n shapedNode) GetLastCommitHash() []byte { return n.lastCommit }

// quietProposer is selectProposer without the account reads and the log line, so
// that a schedule can be searched for: the validator whose share of the total
// stake (stake; nil for equal validators) the seed's hash falls in, among the
// addresses in order.
func quietProposer(tv *testValidators, stake map[string]int64, lastCommit []byte, round int) []byte {
	sorted := append([][]byte(nil), tv.addrs...)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i], sorted[j]) < 0 })
	weight := func(addr []byte) int64 {
		if stake == nil {
			return 1
		}
		return stake[string(addr)]
	}
	total := int64(0)
	for _, addr := range sorted {
		total += weight(addr)
	}
	roundBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(roundBytes, uint64(round))
	seed := sha256.Sum256(append(append([]byte{}, lastCommit...), roundBytes...))
	pick := new(big.Int).Mod(new(big.Int).SetBytes(seed[:]), big.NewInt(total)).Int64()
	for _, addr := range sorted {
		if pick < weight(addr) {
			return addr
		}
		pick -= weight(addr)
	}
	return sorted[0]
}

// lastCommitForSchedule finds a last-commit hash for which the proposer of each
// round in want is the validator with the index it maps to (the schedule depends
// on nothing but this hash, the stakes and the order of the addresses, so it is the
// hash that is searched), and checks the result against the engine's own
// selectProposer, so that a change to how proposers are chosen fails here rather
// than in a test that depends on the schedule.
func lastCommitForSchedule(t *testing.T, tv *testValidators, stake map[string]int64, want map[int]int) []byte {
	t.Helper()
	for attempt := uint64(0); attempt < 5_000_000; attempt++ {
		lastCommit := make([]byte, 8)
		binary.BigEndian.PutUint64(lastCommit, attempt)
		fits := true
		for round, idx := range want {
			if !bytes.Equal(quietProposer(tv, stake, lastCommit, round), tv.addrs[idx]) {
				fits = false
				break
			}
		}
		if !fits {
			continue
		}
		node := shapedNode{signGuardNode: &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}, stake: stake, lastCommit: lastCommit}
		probe := NewEngine(node, tv.keys[0], &captureBroadcaster{})
		for round, idx := range want {
			if !bytes.Equal(probe.selectProposer(round), tv.addrs[idx]) {
				t.Fatalf("test setup: quietProposer disagrees with selectProposer for round %d; it has to follow it", round)
			}
		}
		return lastCommit
	}
	t.Fatalf("no last-commit hash fits the proposer schedule %v", want)
	return nil
}

// firstProposerRound returns the first round from start on in which the validator
// with the given address is the proposer, according to the schedule of engine.
func firstProposerRound(engine *Engine, validators map[string]*big.Int, addr []byte, start int) int {
	for round := start; round < start+10_000; round++ {
		if bytes.Equal(engine.selectProposerIn(validators, round), addr) {
			return round
		}
	}
	return -1
}

// runLoop runs the engine's round loop until the node has committed height 1 (or
// stop is set), the way Start does after its wait.
func runLoop(e *Engine, n *signGuardNode, stop *atomic.Bool) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000 && n.GetHeight() < 1 && !stop.Load(); i++ {
			e.runRound()
		}
	}()
	return done
}

// scenario is one restart: validator 0 (A) keeps running, validator 1 (B) is
// restarted.
type scenario struct {
	t            *testing.T
	tv           *testValidators
	nodeA        *signGuardNode
	nodeB        *signGuardNode
	engA         *Engine
	engB         *Engine
	brA          *meshBroadcaster
	brB          *meshBroadcaster
	signB        string // where B's vote record is kept
	stop         atomic.Bool
	doneA        chan struct{}
	doneB        chan struct{}
	beforeVotesB []*SignedVote // what B signed before it was restarted
}

// newScenario builds A at the round before aRound (so that its first round is
// aRound) and B, listening but not running. shape decides the proposer schedule.
func newScenario(t *testing.T, tv *testValidators, shape shapedNode, aRound int) *scenario {
	t.Helper()
	s := &scenario{t: t, tv: tv, signB: filepath.Join(t.TempDir(), "b_sign_state.json")}
	s.nodeA = &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}
	s.nodeB = &signGuardNode{validatorSet: tv.set, self: tv.addrs[1]}
	nodeFor := func(n *signGuardNode) NodeInterface {
		shaped := shape
		shaped.signGuardNode = n
		return shaped
	}
	s.brA = &meshBroadcaster{}
	s.engA = NewEngine(nodeFor(s.nodeA), tv.keys[0], s.brA, e2eOptions()...)
	s.engA.mu.Lock()
	s.engA.currentState = State{Height: 1, Round: aRound - 1}
	s.engA.mu.Unlock()

	// B, before its restart, was running too: it signed a prevote in round 1 of
	// the height, which is what its vote record (WithSignStatePath) remembers.
	pre := &meshBroadcaster{}
	before := NewEngine(nodeFor(s.nodeB), tv.keys[1], pre, WithSignStatePath(s.signB), WithTimeouts(e2eTimeouts))
	before.mu.Lock()
	before.currentState = State{Height: 1, Round: 1}
	before.mu.Unlock()
	blockOne := candidateBlock(1, 1, tv.addrs[1], 0x01)
	before.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: blockOne, Round: 1, ValidRound: -1}, Proposer: tv.addrs[1]})
	before.prevote()
	s.beforeVotesB = votesBy(pre.messages(), tv.addrs[1])
	if len(s.beforeVotesB) != 1 {
		t.Fatalf("test setup: B should have signed one vote before its restart, got %d", len(s.beforeVotesB))
	}

	// The restart: a new engine on the same node and the same vote record.
	s.brB = &meshBroadcaster{peer: s.engA, peerNode: s.nodeA}
	s.engB = restartedEngine(nodeFor(s.nodeB), tv, s.brB, s.signB)
	s.brA.setPeer(s.engB, s.nodeB)
	return s
}

// restartedEngine is B's engine after its restart: it reads the vote record the
// previous one left (and keeps it in memory, so the guard works as it does live),
// but does not write it back on every vote: a disk sync per vote takes as long here
// as a round of these scaled-down tests, where it takes a small fraction of a live
// round.
func restartedEngine(node NodeInterface, tv *testValidators, br *meshBroadcaster, record string) *Engine {
	engine := NewEngine(node, tv.keys[1], br, e2eOptions(WithSignStatePath(record))...)
	engine.signStatePath = ""
	return engine
}

// startA starts the running validator's rounds.
func (s *scenario) startA() {
	if e2eMinInterval > 0 {
		s.engA.noteCommitSeen()
	}
	s.doneA = runLoop(s.engA, s.nodeA, &s.stop)
}

// bootB lets B listen for e2eBootRounds rounds, then starts its rounds, and
// returns the round A was in at that moment.
func (s *scenario) bootB() int {
	time.Sleep(e2eBootRounds * e2eRound)
	aRound := roundOf(s.engA)
	s.armB()
	s.doneB = runLoop(s.engB, s.nodeB, &s.stop)
	return aRound
}

// startB starts B's rounds at once, and returns the round A was in at that moment.
func (s *scenario) startB() int {
	aRound := roundOf(s.engA)
	s.armB()
	s.doneB = runLoop(s.engB, s.nodeB, &s.stop)
	return aRound
}

// armB, when the test has a minimum block interval, starts B as if it had just taken the
// last block from its peer, so that its first round has the interval to wait out too.
func (s *scenario) armB() {
	if e2eMinInterval > 0 {
		s.engB.noteCommitSeen()
	}
}

// wait blocks until both nodes have committed height 1, or the deadline, and
// stops the loops.
func (s *scenario) wait(within time.Duration) bool {
	deadline := time.After(within)
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	ok := false
loop:
	for {
		select {
		case <-tick.C:
			if s.nodeA.GetHeight() >= 1 && s.nodeB.GetHeight() >= 1 {
				ok = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	s.stop.Store(true)
	endRound(s.engA)
	endRound(s.engB)
	for _, done := range []chan struct{}{s.doneA, s.doneB} {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return ok
}

// commitRound is the round the block of height 1 was committed in.
func (s *scenario) commitRound() int {
	for _, n := range []*signGuardNode{s.nodeA, s.nodeB} {
		n.mu.Lock()
		var round = -1
		if len(n.committed) > 0 && n.committed[0].QuorumCert != nil {
			round = n.committed[0].QuorumCert.Round
		}
		n.mu.Unlock()
		if round >= 0 {
			return round
		}
	}
	return -1
}

// check asserts what every restart must satisfy whatever the timing: both
// validators committed the same block, and B signed no two conflicting votes, and
// none at all in the round it had voted in before the restart or before it.
func (s *scenario) check(votedRound int) {
	s.t.Helper()
	if ha, hb := s.nodeA.committedHash(s.t), s.nodeB.committedHash(s.t); !bytes.Equal(ha, hb) {
		s.t.Fatalf("SAFETY: the validators committed different blocks at height 1: %x and %x", ha, hb)
	}
	all := append(append([]*SignedVote(nil), s.beforeVotesB...), votesBy(s.brB.messages(), s.tv.addrs[1])...)
	requireNoSlashableProof(s.t, all, s.tv.addrs[1])
	for _, v := range votesBy(s.brB.messages(), s.tv.addrs[1]) {
		if v.Vote.Height == 1 && v.Vote.Round <= votedRound {
			s.t.Fatalf("the restarted validator signed a %s in round %d, a round it had already voted in", v.Vote.Type, v.Vote.Round)
		}
	}
	if votes := votesBy(s.brA.messages(), s.tv.addrs[0]); len(votes) > 0 {
		requireNoSlashableProof(s.t, votes, s.tv.addrs[0])
	}
}

// One validator is restarted while the other keeps running at round r, for r
// across 1..30. Block production must resume without waiting for a round the
// restarted validator is not in: it commits in the round of the first proposal the
// running validator makes after the restarted one starts its rounds (or the round
// after it, for a proposal that came just too late in its round) -- never later,
// however many rounds the running validator had gone through alone.
//
// The proposer schedule favours the running validator (its stake is 50 times the
// other's; the voting power stays equal, so both are needed for every quorum), so
// that it makes a proposal in nearly every round: the rounds it went through while
// the other was away are then all rounds a restarted validator that replays what
// it heard would have to walk through one by one.
func TestARestartedValidatorRejoinsARunningPeerAtAnyRound(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	for _, aRound := range []int{1, 2, 3, 5, 8, 9, 12, 17, 24, 30} {
		aRound := aRound
		t.Run(fmt.Sprintf("running_at_round_%d", aRound), func(t *testing.T) {
			tv := newTestValidators(t, 2)
			shape := shapedNode{stake: map[string]int64{string(tv.addrs[0]): 50, string(tv.addrs[1]): 1}}
			s := newScenario(t, tv, shape, aRound)
			s.startA()
			atStart := s.bootB()

			// The first proposal the running validator makes from the next round on.
			bound := firstProposerRound(s.engA, tv.set, tv.addrs[0], atStart+1) + 1
			started := time.Now()
			if !s.wait(time.Duration(bound-atStart+8) * e2eRound * 4) {
				t.Fatalf("LIVENESS: no block after the restart: A at height %d round %d, B at height %d round %d (A was in round %d when B started)",
					s.nodeA.GetHeight(), roundOf(s.engA), s.nodeB.GetHeight(), roundOf(s.engB), atStart)
			}
			took := time.Since(started)
			round := s.commitRound()
			t.Logf("A was in round %d when B started its rounds; the block committed in round %d after %v (%d round timeouts)", atStart, round, took, int(took/e2eRound))
			if round > bound {
				t.Fatalf("the block committed in round %d, want by round %d: the restarted validator did not rejoin at once (A was in round %d when it started)", round, bound, atStart)
			}
			s.check(1)
		})
	}
}

// scheduleOf is the schedule of proposers of the rounds from..to, A or B, from
// the point of view of engine.
func scheduleOf(engine *Engine, validators map[string]*big.Int, tv *testValidators, from, to int) string {
	var out []byte
	for round := from; round <= to; round++ {
		proposer := engine.selectProposerIn(validators, round)
		switch {
		case bytes.Equal(proposer, tv.addrs[0]):
			out = append(out, 'A')
		case bytes.Equal(proposer, tv.addrs[1]):
			out = append(out, 'B')
		default:
			out = append(out, '?')
		}
	}
	return string(out)
}

// lockedScenario is the stall with a lock: validator A locked on block X at round 7 (both
// validators prevoted it, A saw both prevotes; B was restarted before it saw A's),
// went through rounds 8 to 17 alone, and B, which knows nothing of X and proposes
// blocks of its own when it is the proposer, is back.
type lockedScenario struct {
	*scenario
	shape  shapedNode
	blockX *types.Block
	hashX  []byte
}

// newLockedScenario builds the stall with a lock: A locked at round 7, its first
// round from then on being firstRound (8 when B is back at once, 18 when A has gone
// through rounds 8 to 17 alone).
func newLockedScenario(t *testing.T, tv *testValidators, shape shapedNode, firstRound int) *lockedScenario {
	t.Helper()
	s := &lockedScenario{shape: shape}
	s.scenario = newScenario(t, tv, shape, firstRound)
	s.blockX = candidateBlock(1, 7, tv.addrs[0], 0x58)
	s.hashX = headerHash(t, s.blockX)

	// A's view of round 7: it prevoted X, then B's prevote for X arrived: a polka,
	// and A is locked on X.
	a := s.engA
	a.mu.Lock()
	a.currentState = State{Height: 1, Round: 7}
	a.mu.Unlock()
	if !a.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: s.blockX, Round: 7, ValidRound: -1}, Proposer: tv.addrs[0]}) {
		t.Fatalf("test setup: A did not accept round 7's proposal")
	}
	a.prevote()
	a.addVoteIfRelevant(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, s.hashX, 7, 1))
	if _, _, locked := a.Status(); locked != 7 {
		t.Fatalf("test setup: A is not locked at round 7 (locked round %d)", locked)
	}
	// B's record: it prevoted X in round 7, and was restarted before anything else.
	s.rewriteBRecord(7)
	// A moves on: its first round is firstRound.
	a.mu.Lock()
	a.currentState.Round = firstRound - 1
	a.mu.Unlock()
	return s
}

// rewriteBRecord makes B's vote record say it prevoted X in round.
func (s *lockedScenario) rewriteBRecord(round int) {
	s.t.Helper()
	pre := &meshBroadcaster{}
	nodeB := s.shape
	nodeB.signGuardNode = s.nodeB
	before := NewEngine(nodeB, s.tv.keys[1], pre, WithSignStatePath(s.signB), WithTimeouts(e2eTimeouts))
	// The record from the setup is for round 1: a later round replaces it.
	before.mu.Lock()
	before.currentState = State{Height: 1, Round: round}
	before.mu.Unlock()
	before.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: s.blockX, Round: round, ValidRound: -1}, Proposer: s.tv.addrs[0]})
	before.prevote()
	s.beforeVotesB = append(s.beforeVotesB, votesBy(pre.messages(), s.tv.addrs[1])...)
	// The engine that is started later reads the record again.
	s.engB = restartedEngine(nodeB, s.tv, s.brB, s.signB)
	s.brA.setPeer(s.engB, s.nodeB)
}

// The stall with a lock, on two real engines: A is locked on X at round 7 and has gone
// through rounds 8 to 17 alone; B, restarted, knows nothing of X, and is the
// proposer of every round from its first: it offers a block of its own each time,
// which A, locked on X, refuses, and A is never the proposer (the stakes are 50
// to 1 for B, and the keys and hash are drawn until B is the proposer of rounds 8
// to 45). Nothing but the locked validator's own turn could commit anything.
//
// The height must commit -- X, the only block that can still commit -- within a few
// rounds of B starting its rounds: B's first message reaches A in a round A has
// passed, which is how A learns B is behind and passes its polka on; B learns X,
// and the round A is in, and re-proposes X there.
func TestALockedValidatorAndARestartedPeerCommitWithinFewRounds(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	tv := newTestValidators(t, 2)
	stake := map[string]int64{string(tv.addrs[0]): 1, string(tv.addrs[1]): 50}
	want := map[int]int{}
	for round := 8; round <= 45; round++ {
		want[round] = 1
	}
	shape := shapedNode{stake: stake, lastCommit: lastCommitForSchedule(t, tv, stake, want)}

	s := newLockedScenario(t, tv, shape, 18)
	t.Logf("schedule of proposers, rounds 8-45: %s", scheduleOf(s.engA, tv.set, tv, 8, 45))
	s.startA()
	atStart := s.bootB()

	// B proposes in its first round (8); A's first sight of a message of it, and
	// the relay, come at once, and B is the proposer wherever it lands.
	bound := atStart + 2
	started := time.Now()
	if !s.wait(time.Duration(bound-atStart+8) * e2eRound * 4) {
		t.Fatalf("LIVENESS: height 1 did not commit after B came back: A at height %d round %d, B at height %d round %d (A was in round %d when B started)",
			s.nodeA.GetHeight(), roundOf(s.engA), s.nodeB.GetHeight(), roundOf(s.engB), atStart)
	}
	took := time.Since(started)
	round := s.commitRound()
	t.Logf("A (locked at round 7) was in round %d when B started its rounds; the block committed in round %d after %v (%d round timeouts)", atStart, round, took, int(took/e2eRound))
	if round > bound {
		t.Fatalf("the block committed in round %d, want by round %d (A was in round %d when B started its rounds)", round, bound, atStart)
	}
	if got := s.nodeA.committedHash(t); !bytes.Equal(got, s.hashX) {
		t.Fatalf("the committed block is %x, want the locked block X %x: a validator locked on X must never see another block commit", got, s.hashX)
	}
	s.check(7)
}

// The same stall with the two validators in the same rounds from the start: A is
// locked on X at round 7 and B, restarted, knows nothing of X. B is the proposer of
// rounds 8 to 17 and offers a different block in each, and A's own turn is round
// 18 (the keys and hash are drawn until it is so; the two stakes are equal). A,
// locked on X, refuses every block but X, so without help the height commits at
// round 18, ten rounds on.
//
// A refuses B's block of round 8 and passes its polka on with the refusal; B
// learns X, and re-proposes it in round 9, which A accepts.
func TestALockedValidatorRefusesAtMostOneBlockBeforeThePeerReProposesItsOwn(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	tv := newTestValidators(t, 2)
	want := map[int]int{18: 0}
	for round := 8; round <= 17; round++ {
		want[round] = 1
	}
	shape := shapedNode{lastCommit: lastCommitForSchedule(t, tv, nil, want)}

	s := newLockedScenario(t, tv, shape, 8)
	t.Logf("schedule of proposers, rounds 8-18: %s", scheduleOf(s.engA, tv.set, tv, 8, 18))
	s.startA()
	atStart := s.startB()

	// Round 8 (B's block refused, the polka passed on), round 9 (X re-proposed);
	// one round more for a message that came just too late in its round.
	bound := 8 + 3
	started := time.Now()
	if !s.wait(time.Duration(18-atStart+4) * e2eRound * 4) {
		t.Fatalf("LIVENESS: height 1 did not commit: A at height %d round %d, B at height %d round %d",
			s.nodeA.GetHeight(), roundOf(s.engA), s.nodeB.GetHeight(), roundOf(s.engB))
	}
	took := time.Since(started)
	round := s.commitRound()
	t.Logf("the block committed in round %d after %v (%d round timeouts)", round, took, int(took/e2eRound))
	if round > bound {
		t.Fatalf("the block committed in round %d, want by round %d: the locked validator refused blocks until its own turn", round, bound)
	}
	if got := s.nodeA.committedHash(t); !bytes.Equal(got, s.hashX) {
		t.Fatalf("the committed block is %x, want the locked block X %x", got, s.hashX)
	}
	s.check(7)
}

// A restarted validator never signs two different votes in a round, whatever
// rounds it is moved through. It comes back knowing it voted in round 12; the
// evidence first names round 12 (where it already voted) and then round 20; it
// signs only in rounds after 12, once each.
func TestARestartedValidatorNeverSignsTwiceInARoundWhenItIsMoved(t *testing.T) {
	tv := newTestValidators(t, 4)
	path := filepath.Join(t.TempDir(), "sign.json")
	node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}

	// Before the restart: prevote in round 12 for block P.
	brBefore := &captureBroadcaster{}
	before := NewEngine(node, tv.keys[0], brBefore, WithSignStatePath(path))
	before.mu.Lock()
	before.currentState = State{Height: 1, Round: 12}
	before.mu.Unlock()
	blockP := candidateBlock(1, 12, tv.addrs[1], 0x0A)
	before.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: blockP, Round: 12, ValidRound: -1}, Proposer: tv.addrs[1]})
	before.prevote()

	// After it: two other validators are seen at round 12 (where it already voted),
	// which must not take it back there.
	brAfter := &captureBroadcaster{}
	engine := NewEngine(node, tv.keys[0], brAfter, WithSignStatePath(path))
	engine.mu.Lock()
	engine.recordRoundClaimLocked(tv.addrs[1], 1, 12)
	engine.recordRoundClaimLocked(tv.addrs[2], 1, 12)
	engine.mu.Unlock()
	engine.startNewRound()
	if got := roundOf(engine); got <= 12 {
		t.Fatalf("the restarted validator is in round %d, a round it voted in (or before it)", got)
	}

	// Then the others are seen at round 20: it moves there and votes there, once.
	engine.mu.Lock()
	engine.recordRoundClaimLocked(tv.addrs[1], 1, 20)
	engine.recordRoundClaimLocked(tv.addrs[2], 1, 20)
	engine.mu.Unlock()
	engine.startNewRound()
	if got := roundOf(engine); got != 20 {
		t.Fatalf("the restarted validator is in round %d, want 20", got)
	}
	blockQ := candidateBlock(1, 20, tv.addrs[2], 0x0C)
	engine.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: blockQ, Round: 20, ValidRound: -1}, Proposer: tv.addrs[2]})
	engine.prevote()
	engine.prevote()

	all := append(votesBy(brBefore.messages(), tv.addrs[0]), votesBy(brAfter.messages(), tv.addrs[0])...)
	requireNoSlashableProof(t, all, tv.addrs[0])
	for _, v := range votesBy(brAfter.messages(), tv.addrs[0]) {
		if v.Vote.Round <= 12 {
			t.Fatalf("the restarted validator signed a %s in round %d, a round it had already voted in", v.Vote.Type, v.Vote.Round)
		}
	}
	after := votesBy(brAfter.messages(), tv.addrs[0])
	if len(after) != 1 || after[0].Vote.Round != 20 || !bytes.Equal(after[0].Vote.BlockHash, headerHash(t, blockQ)) {
		t.Fatalf("the restarted validator should have prevoted block Q once, in round 20; it sent %s", describeVotes(after))
	}
}
