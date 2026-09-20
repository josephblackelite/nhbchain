package bft

// What the engine keeps for a height is not used, or kept, once the height has moved on.
//
// Who proposes a round depends on the chain state at the height (the hash of the last
// block, the stakes) as well as on the round, and proposerFor keeps what it works out
// so that a stream of proposals does not cost a state read each. It used to key what it
// kept by the round alone, and to forget it only when the next round started. But
// commit() moves the height on, the round loop starts the next round only after commit
// returns, and a proposal that was waiting for the engine's lock (which commit holds
// while the node commits) is handled in between: it was compared with the proposer of
// the height before and, when the two differ (about one time in two with two
// validators), dropped as coming from a validator that is not the round's proposer --
// neither queued nor buffered -- while the height before's proposer, who proposes
// nothing now, was let in. The validator then saw no proposal in a round in which its
// peer had made one, sent no prevote for it, and the round failed when its timers ended.
//
// The messages kept for rounds a validator has not reached had the same flaw in another
// form: they were forgotten by round but never by height, so what a peer sent for the
// rounds this validator never reached stayed for as long as the process ran, at every
// height, and the bound on what one peer can make it hold was a bound at each height
// and none over time.
//
// Each test below drives the engine's real entry points (commit, HandleProposal,
// startNewRound, the round loop) and fails on the engine that keeps a proposer by round
// alone and forgets the messages of a height only when the node moves the height.

import (
	"bytes"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"nhbchain/core/types"
)

// chainTipNode is a node whose last block has a different hash at every height, as a
// chain's has, so that the proposer of a round differs from one height to the next
// (signGuardNode answers with no hash at all, so the same validator would propose the
// same round at every height). tips is indexed by the number of blocks the chain has:
// it is the hash the proposer of the next height is chosen by.
type chainTipNode struct {
	*signGuardNode
	tips map[uint64][]byte
}

func (n chainTipNode) GetLastCommitHash() []byte { return n.tips[n.GetHeight()] }

// tipsForProposers returns last-block hashes for a chain of the validators tv for
// which the proposer of round at height h+1 is the validator with index proposers[h].
func tipsForProposers(t *testing.T, tv *testValidators, round int, proposers ...int) map[uint64][]byte {
	t.Helper()
	tips := make(map[uint64][]byte, len(proposers))
	for blocks, idx := range proposers {
		tips[uint64(blocks)] = lastCommitForSchedule(t, tv, nil, map[int]int{round: idx})
	}
	return tips
}

// newChainEngine builds the engine of validator 0 of tv, on a chain with no blocks.
func newChainEngine(t *testing.T, tv *testValidators, tips map[uint64][]byte) (*Engine, *signGuardNode) {
	t.Helper()
	node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}
	return NewEngine(chainTipNode{node, tips}, tv.keys[0], &captureBroadcaster{}), node
}

// inRound puts the engine in the round of the height.
func inRound(e *Engine, height uint64, round int) {
	e.mu.Lock()
	e.currentState = State{Height: height, Round: round}
	e.mu.Unlock()
}

// decideHeight takes the engine, which is in the round of the height, up to the moment
// it commits: the round's proposer (worked out the way the round loop does at the top
// of a round) has proposed a block and every validator has voted for it. It returns the
// index of the proposer; committing is left to the caller.
func decideHeight(t *testing.T, tv *testValidators, e *Engine, height uint64, round int) int {
	t.Helper()
	proposer := e.proposerFor(height, round)
	idx := -1
	for i := range tv.addrs {
		if bytes.Equal(tv.addrs[i], proposer) {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("test setup: no validator is the proposer of round %d at height %d (%x)", round, height, proposer)
	}
	block := candidateBlock(height, round, tv.addrs[idx], 0x11)
	hash := headerHash(t, block)
	if !e.acceptProposal(newSignedProposal(t, tv.keys[idx], tv.addrs[idx], block, round)) {
		t.Fatalf("test setup: the proposal of height %d was not accepted", height)
	}
	for _, vt := range []VoteType{Prevote, Precommit} {
		for i := range tv.keys {
			e.addVoteIfRelevant(signVoteAs(t, tv.keys[i], tv.addrs[i], vt, hash, round, height))
		}
	}
	return idx
}

// proposersOf lists who signed the proposals, for a failure message.
func proposersOf(tv *testValidators, proposals []*SignedProposal) string {
	out := make([]int, 0, len(proposals))
	for _, p := range proposals {
		idx := -1
		for i := range tv.addrs {
			if bytes.Equal(tv.addrs[i], p.Proposer) {
				idx = i
			}
		}
		out = append(out, idx)
	}
	return fmt.Sprintf("%v", out)
}

// Validators 0 (this one), 1 and 2 have committed height 1 -- whose proposer was 2 --
// and the proposals for height 2 arrive. The proposer of height 2 is 1: its proposal is
// kept for the round loop, whichever round it is for and whether or not the round loop
// has started the next round yet, and the proposal of 2, the proposer of the height
// before, is not.
func TestTheProposalOfTheNextHeightsProposerIsKeptAfterACommit(t *testing.T) {
	cases := []struct {
		name    string
		round   int  // the round height 1 was decided in, and the round of the proposals for height 2
		started bool // the round loop has started the next round when the proposals arrive
	}{
		// commit leaves the engine in round 0 of the next height.
		{"for the round the commit leaves the engine in", 0, false},
		// Every height after the first starts in round 1 (the round loop moves on one
		// round from the round 0 commit leaves), so that is the round a peer's proposal
		// is for, and this validator is not in it yet.
		{"for the round a height starts in, ahead of the engine", 1, false},
		{"after the round loop has started the next round", 1, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tv := newTestValidators(t, 3)
			engine, _ := newChainEngine(t, tv, tipsForProposers(t, tv, tc.round, 2, 1))
			inRound(engine, 1, tc.round)
			if got := decideHeight(t, tv, engine, 1, tc.round); got != 2 {
				t.Fatalf("test setup: validator %d proposed height 1, want 2", got)
			}
			if !engine.commit() {
				t.Fatalf("test setup: height 1 did not commit")
			}
			if tc.started {
				engine.startNewRound()
			}

			real := newSignedProposal(t, tv.keys[1], tv.addrs[1], candidateBlock(2, tc.round, tv.addrs[1], 0x22), tc.round)
			previous := newSignedProposal(t, tv.keys[2], tv.addrs[2], candidateBlock(2, tc.round, tv.addrs[2], 0x23), tc.round)
			for _, p := range []*SignedProposal{previous, real} {
				if err := engine.HandleProposal(p); err != nil {
					t.Fatalf("proposal of validator %s: %v", proposersOf(tv, []*SignedProposal{p}), err)
				}
			}
			if !tc.started && tc.round > 0 {
				// The round loop's next round start, and the replay it does at the top of the round.
				engine.startNewRound()
				engine.replayBufferedMessages(2, tc.round)
			}
			if got := roundOf(engine); got != tc.round {
				t.Fatalf("test setup: the engine is in round %d, want %d", got, tc.round)
			}

			queued := drainProposals(engine)
			fromProposer, fromPrevious := 0, 0
			for _, p := range queued {
				switch {
				case bytes.Equal(p.Proposer, tv.addrs[1]):
					fromProposer++
				case bytes.Equal(p.Proposer, tv.addrs[2]):
					fromPrevious++
				}
			}
			if fromProposer != 1 {
				t.Fatalf("the proposal of validator 1, the proposer of height 2, was not given to the round loop (it was given the proposals of validators %s)", proposersOf(tv, queued))
			}
			if fromPrevious != 0 {
				t.Fatalf("the proposal of validator 2, who proposed height 1 and does not propose height 2, was given to the round loop (it was given the proposals of validators %s)", proposersOf(tv, queued))
			}
		})
	}
}

// gatedCommitNode holds the node's commit open until the test lets it go.
type gatedCommitNode struct {
	chainTipNode
	started chan struct{} // closed when CommitBlock is entered
	release chan struct{} // CommitBlock does not return until this is closed
}

func (n *gatedCommitNode) CommitBlock(b *types.Block) error {
	close(n.started)
	<-n.release
	return n.signGuardNode.CommitBlock(b)
}

// The real interleaving: the proposal for the next height arrives while the engine is
// inside commit(), waits for the lock commit holds, and is handled the moment commit
// returns -- before the round loop, which has to get from commit's return to the next
// round start, has started it. It is kept whichever of the two is first. The pause
// stands for what can delay the round loop between the two (a garbage collection, a
// busy machine): with none the two race, and the proposal is lost only when the
// handler wins, which it rarely does by a few microseconds; with one the handler
// always wins.
func TestAProposalThatArrivesWhileTheEngineIsCommittingIsKept(t *testing.T) {
	const round, trials = 1, 20
	tv := newTestValidators(t, 3)
	tips := tipsForProposers(t, tv, round, 2, 1)
	real := newSignedProposal(t, tv.keys[1], tv.addrs[1], candidateBlock(2, round, tv.addrs[1], 0x22), round)

	for _, pause := range []time.Duration{0, time.Millisecond} {
		pause := pause
		t.Run(fmt.Sprintf("round_loop_pause_%v", pause), func(t *testing.T) {
			lost := 0
			for trial := 0; trial < trials; trial++ {
				node := &gatedCommitNode{
					chainTipNode: chainTipNode{&signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}, tips},
					started:      make(chan struct{}),
					release:      make(chan struct{}),
				}
				engine := NewEngine(node, tv.keys[0], &captureBroadcaster{})
				inRound(engine, 1, round)
				if got := decideHeight(t, tv, engine, 1, round); got != 2 {
					t.Fatalf("test setup: validator %d proposed height 1, want 2", got)
				}

				committed := make(chan bool, 1)
				go func() { committed <- engine.commit() }()
				<-node.started // commit holds the engine's lock, inside the node's commit

				handled := make(chan error, 1)
				go func() { handled <- engine.HandleProposal(real) }()
				time.Sleep(10 * time.Millisecond) // the handler is waiting for the lock
				close(node.release)
				if !<-committed {
					t.Fatalf("trial %d: height 1 did not commit", trial)
				}
				time.Sleep(pause)
				engine.startNewRound() // what the round loop does when commit returns, while the handler runs
				if err := <-handled; err != nil {
					t.Fatalf("trial %d: proposal: %v", trial, err)
				}
				engine.replayBufferedMessages(2, round)

				queued := drainProposals(engine)
				if len(queued) != 1 || !bytes.Equal(queued[0].Proposer, tv.addrs[1]) {
					lost++
				}
			}
			if lost > 0 {
				t.Fatalf("the proposer's proposal for height 2 was lost in %d of %d trials: it arrived while the engine committed height 1", lost, trials)
			}
		})
	}
}

// A proposer worked out for a height is not used after the node has moved the height on
// some other way than this engine's own commit (a block committed through the peer
// sync path is found by syncHeightWithNodeLocked at the next round start, and at the
// end of commit).
func TestProposersWorkedOutForAHeightAreNotUsedAfterTheNodeMovesTheHeightOn(t *testing.T) {
	const round = 2
	tv := newTestValidators(t, 3)
	engine, node := newChainEngine(t, tv, tipsForProposers(t, tv, round, 2, 1))
	inRound(engine, 1, round)
	if got := engine.proposerFor(1, round); !bytes.Equal(got, tv.addrs[2]) {
		t.Fatalf("test setup: the proposer of round %d at height 1 is %x, want validator 2", round, got)
	}

	node.applySynced(candidateBlock(1, round, tv.addrs[2], 0x11))
	engine.mu.Lock()
	moved := engine.syncHeightWithNodeLocked()
	height := engine.currentState.Height
	engine.mu.Unlock()
	if !moved || height != 2 {
		t.Fatalf("test setup: the engine did not move to height 2 (moved=%v height=%d)", moved, height)
	}
	if got := engine.proposerFor(2, round); !bytes.Equal(got, tv.addrs[1]) {
		t.Fatalf("the proposer of round %d at height 2 is validator 1; the engine says %x (validator 2 proposed height 1)", round, got)
	}
}

// countingNode counts the account reads a choice of proposer costs.
type countingNode struct {
	chainTipNode
	reads *atomic.Int64
}

func (n countingNode) GetAccount(addr []byte) (*types.Account, error) {
	n.reads.Add(1)
	return n.signGuardNode.GetAccount(addr)
}

// What the cache is for stays: a stream of proposals costs one choice of proposer for
// each height and round, not one for each proposal, at the height the engine is at and
// after it has moved on.
func TestTheProposerIsWorkedOutOncePerHeightAndRoundNotOncePerMessage(t *testing.T) {
	const round, spam = 1, 100
	tv := newTestValidators(t, 3)
	tips := tipsForProposers(t, tv, round, 2, 1)
	reads := new(atomic.Int64)
	node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}
	engine := NewEngine(countingNode{chainTipNode{node, tips}, reads}, tv.keys[0], &captureBroadcaster{})

	sendFrom := func(idx int, height uint64, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			p := newSignedProposal(t, tv.keys[idx], tv.addrs[idx], candidateBlock(height, round, tv.addrs[idx], byte(i)), round)
			if err := engine.HandleProposal(p); err != nil {
				t.Fatalf("proposal %d: %v", i, err)
			}
		}
	}

	inRound(engine, 1, round)
	decideHeight(t, tv, engine, 1, round) // the round loop's own choice at the top of the round
	perChoice := reads.Load()
	if perChoice == 0 {
		t.Fatalf("test setup: choosing a proposer read no account")
	}
	sendFrom(1, 1, spam) // not the proposer of round 1 at height 1: dropped, and its proposals are the point
	if got := reads.Load(); got != perChoice {
		t.Fatalf("%d proposals for one round cost %d account reads, want the %d of one choice", spam, got, perChoice)
	}

	if !engine.commit() {
		t.Fatalf("test setup: height 1 did not commit")
	}
	engine.startNewRound() // round 1 of height 2
	sendFrom(2, 2, spam)   // not the proposer at height 2 either
	if got := reads.Load(); got != 2*perChoice {
		t.Fatalf("%d proposals for a round of the next height cost %d account reads in all, want the %d of two choices", spam, got, 2*perChoice)
	}
}

// Whatever is kept of messages for rounds a validator did not reach is kept only for the
// height it is at: when the height moves on the rest is forgotten.
func TestMessagesKeptForACommittedHeightAreForgotten(t *testing.T) {
	const round, ahead = 1, 3
	tv := validatorsWithSchedule(t, 2, map[int]int{ahead: 1})
	engine, _, _ := engineAt(t, tv, round)

	// Validator 1, ahead by two rounds, is the proposer of round 3: what it sent for it is kept.
	if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(ahead, tv.addrs[1], 0x31), ahead)); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	sendVote(t, engine, tv, 1, ahead)
	engine.mu.RLock()
	keptProposals, keptVotes := len(engine.bufferedProposal[1][ahead]), len(engine.bufferedVotes[1][ahead])
	engine.mu.RUnlock()
	if keptProposals != 1 || keptVotes != 1 {
		t.Fatalf("test setup: %d proposals and %d votes are kept for round %d, want 1 and 1", keptProposals, keptVotes, ahead)
	}

	// Height 1 is decided in round 1 and committed; the next round starts.
	decideHeight(t, tv, engine, 1, round)
	if !engine.commit() {
		t.Fatalf("test setup: height 1 did not commit")
	}
	engine.startNewRound()

	engine.mu.RLock()
	heldProposals, heldVotes := len(engine.bufferedProposal), len(engine.bufferedVotes)
	engine.mu.RUnlock()
	if heldProposals != 0 || heldVotes != 0 {
		t.Fatalf("after height 1 was committed the engine still keeps the messages of %d heights of proposals and %d of votes, want none", heldProposals, heldVotes)
	}
}

// What one validator can make another hold is bounded over time, not only at each
// height: a validator that sends two large proposals for a round ahead, the most that
// is kept for it, at every height leaves nothing behind when the height is committed,
// so after forty heights the heap holds one height's worth at most, not forty (about
// 72 MB).
func TestLargeFutureProposalsAtEveryHeightDoNotGrowTheHeapWithoutBound(t *testing.T) {
	const heights, ahead = 40, 3
	tv := validatorsWithSchedule(t, 2, map[int]int{ahead: 1})
	engine, _, _ := engineAt(t, tv, 0)
	payload := make([]byte, 900*1024)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for height := uint64(1); height <= heights; height++ {
		engine.startNewRound() // round 1: where every height starts
		for salt := byte(0); salt < maxBufferedProposalsPerValidator; salt++ {
			header := &types.BlockHeader{Height: height, Validator: tv.addrs[1], PrevHash: append([]byte{byte(height), salt}, payload...)}
			if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], types.NewBlock(header, nil), ahead)); err != nil {
				t.Fatalf("height %d: proposal %d: %v", height, salt, err)
			}
		}
		decideHeight(t, tv, engine, height, 1)
		if !engine.commit() {
			t.Fatalf("height %d did not commit", height)
		}
	}
	engine.startNewRound() // the round after the last commit
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(engine) // what the engine holds is what is measured
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if limit := int64(24 << 20); grew > limit {
		t.Fatalf("the heap grew by %d MB over %d heights of proposals for rounds ahead, want under %d MB", grew>>20, heights, limit>>20)
	}
}

// slowCommitNode takes delay to commit a block.
type slowCommitNode struct {
	chainTipNode
	delay time.Duration
}

func (n slowCommitNode) CommitBlock(b *types.Block) error {
	time.Sleep(n.delay)
	return n.signGuardNode.CommitBlock(b)
}

// runHeights runs the engine's round loop, the way Start does after its wait, until
// the node has committed target blocks (or stop is set), pausing for pause between
// one round and the next (see the test that uses it).
func runHeights(e *Engine, n *signGuardNode, target uint64, pause time.Duration, stop *atomic.Bool) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000 && n.GetHeight() < target && !stop.Load(); i++ {
			e.runRound()
			time.Sleep(pause)
		}
	}()
	return done
}

// Two real engines over real messages and real (scaled) timers, one of which commits
// slowly, decide four heights. The validator that commits first moves to the next
// height and proposes at once, and the proposal reaches the slow one while it is still
// committing: it is not dropped, so every height is decided in its first round (round
// 1: a height starts there) and no round has to time out. B proposes the odd heights,
// so that at each even one the proposer B worked out at the height before is not the
// proposer of the round, and the proposal is A's. The round loops pause for a
// millisecond between one round and the next, which is what can delay a round loop
// there (a garbage collection, a busy machine): without it the proposal is handled
// before the round loop has started the next round only by a few microseconds and
// rarely, and with it always.
func TestTwoValidatorsDecideEveryHeightInItsFirstRoundWhenOneCommitsSlowly(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	const heights = 4
	timers := TimeoutConfig{Proposal: 200 * time.Millisecond, Prevote: 200 * time.Millisecond, Precommit: 200 * time.Millisecond, Commit: 400 * time.Millisecond}
	tv := newTestValidators(t, 2)
	tips := tipsForProposers(t, tv, 1, 1, 0, 1, 0)
	nodeA := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}
	nodeB := &signGuardNode{validatorSet: tv.set, self: tv.addrs[1]}
	brA, brB := &meshBroadcaster{}, &meshBroadcaster{}
	engA := NewEngine(chainTipNode{nodeA, tips}, tv.keys[0], brA, WithTimeouts(timers))
	engB := NewEngine(slowCommitNode{chainTipNode{nodeB, tips}, 15 * time.Millisecond}, tv.keys[1], brB, WithTimeouts(timers))
	// No node is given, so that only the proposals and votes are delivered: each engine
	// has to commit through its own rounds, not a block its peer sends.
	brA.setPeer(engB, nil)
	brB.setPeer(engA, nil)

	var stop atomic.Bool
	doneA := runHeights(engA, nodeA, heights, time.Millisecond, &stop)
	doneB := runHeights(engB, nodeB, heights, time.Millisecond, &stop)
	deadline := time.After(30 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
wait:
	for {
		select {
		case <-tick.C:
			if nodeA.GetHeight() >= heights && nodeB.GetHeight() >= heights {
				break wait
			}
		case <-deadline:
			break wait
		}
	}
	stop.Store(true)
	engA.NotifyExternalCommit()
	engB.NotifyExternalCommit()
	for _, done := range []chan struct{}{doneA, doneB} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}

	roundsOf := func(n *signGuardNode) []int {
		n.mu.Lock()
		defer n.mu.Unlock()
		var rounds []int
		for _, b := range n.committed {
			round := -1
			if b.QuorumCert != nil {
				round = b.QuorumCert.Round
			}
			rounds = append(rounds, round)
		}
		return rounds
	}
	roundsA, roundsB := roundsOf(nodeA), roundsOf(nodeB)
	if len(roundsA) < heights || len(roundsB) < heights {
		t.Fatalf("LIVENESS: A committed %d heights and B %d of %d", len(roundsA), len(roundsB), heights)
	}
	for h := 0; h < heights; h++ {
		if roundsA[h] != 1 || roundsB[h] != 1 {
			t.Fatalf("height %d was decided in round %d at A and %d at B, want round 1: a proposal was lost and its round had to time out (rounds by height: A %v, B %v)",
				h+1, roundsA[h], roundsB[h], roundsA, roundsB)
		}
	}
}
