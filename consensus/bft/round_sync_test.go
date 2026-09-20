package bft

// Round synchronisation regression tests (see round_sync.go).
//
// Before the window, the bounded buffer and the evidence rule, a validator kept
// every message for a round it had not reached, without limit, and at its next
// round start moved to the lowest round any of them named. So one signed vote for
// round 1,000,000 from one validator stranded it there; a stream of large
// proposals for different rounds grew its heap without bound; more than sixteen
// proposals from validators that were not the round's proposer, sent ahead of the
// real one, pushed the real one out of the replay queue; and a validator that came
// back after a restart walked through the rounds its peer had already left. Each
// test below drives the real entry points (HandleVote, HandleProposal,
// startNewRound, the round loop) and fails on the engine without the fix.

import (
	"bytes"
	"context"
	"log/slog"
	"math/big"
	"runtime"
	"sync"
	"testing"
	"time"

	"nhbchain/core/types"
)

// newWeightedValidators is a set of validators with the given voting powers.
func newWeightedValidators(t *testing.T, powers ...int64) *testValidators {
	t.Helper()
	tv := newTestValidators(t, len(powers))
	for i, power := range powers {
		tv.set[string(tv.addrs[i])] = big.NewInt(power)
	}
	return tv
}

// engineAt builds the engine of validator 0 of tv, at height 1 and round.
func engineAt(t *testing.T, tv *testValidators, round int, opts ...Option) (*Engine, *signGuardNode, *captureBroadcaster) {
	t.Helper()
	engine, node, br := newVoter(t, tv, opts...)
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: round}
	engine.mu.Unlock()
	return engine, node, br
}

func roundOf(e *Engine) int {
	_, round, _ := e.Status()
	return round
}

func bufferedVoteCount(e *Engine, round int) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.bufferedVotes[1][round])
}

func bufferedProposalCount(e *Engine) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	n := 0
	for _, proposals := range e.bufferedProposal[1] {
		n += len(proposals)
	}
	return n
}

// sendVote hands the engine a signed prevote of validator idx for round.
func sendVote(t *testing.T, e *Engine, tv *testValidators, idx int, round int) {
	t.Helper()
	hash := sha256Sum([]byte("some block"))
	if err := e.HandleVote(signVoteAs(t, tv.keys[idx], tv.addrs[idx], Prevote, hash, round, 1)); err != nil {
		t.Fatalf("vote of validator %d for round %d: %v", idx, round, err)
	}
}

// One validator's vote for a round far ahead must not move this one there, and
// must not make it lose the votes of the round the others are in.
func TestOneFutureVoteDoesNotStrandAValidator(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 0)

	const far = 1_000_000
	sendVote(t, engine, tv, 1, far)
	runOneRound(engine) // round 1 ends by its timer
	engine.startNewRound()

	if got := roundOf(engine); got != 2 {
		t.Fatalf("one validator's vote for round %d moved this validator to round %d, want 2", far, got)
	}
	// The honest network is at round 2: its votes are queued, not dropped.
	before := len(engine.voteCh)
	sendVote(t, engine, tv, 2, 2)
	if len(engine.voteCh) != before+1 {
		t.Fatalf("a vote for the round this validator is in was not queued")
	}
	if n := bufferedVoteCount(engine, far); n != 0 {
		t.Fatalf("the vote for round %d was buffered (%d held)", far, n)
	}
}

// A vote or a proposal is kept for a future round only within maxFutureRounds of
// the round the validator is in; beyond it, it is dropped, never buffered.
func TestMessagesBeyondTheWindowAreDroppedNotBuffered(t *testing.T) {
	const current = 3
	edge, beyond := current+maxFutureRounds, current+maxFutureRounds+1

	t.Run("votes", func(t *testing.T) {
		tv := newTestValidators(t, 4)
		engine, _, _ := engineAt(t, tv, current)
		// The same validator both times: one validator alone is no evidence for
		// anything (the next case has two).
		sendVote(t, engine, tv, 1, edge)
		sendVote(t, engine, tv, 1, beyond)
		if n := bufferedVoteCount(engine, edge); n != 1 {
			t.Fatalf("the vote at the edge of the window (round %d) was not kept: %d held", edge, n)
		}
		if n := bufferedVoteCount(engine, beyond); n != 0 {
			t.Fatalf("a vote for round %d, beyond the window of %d rounds from round %d, was buffered", beyond, maxFutureRounds, current)
		}
	})

	// Two validators of four far ahead are evidence that moves the validator (the
	// next test), but the window does not move with them: what they sent beyond it is
	// still dropped.
	t.Run("votes even when they move the validator", func(t *testing.T) {
		tv := newTestValidators(t, 4)
		engine, _, _ := engineAt(t, tv, current)
		sendVote(t, engine, tv, 1, beyond)
		sendVote(t, engine, tv, 2, beyond)
		if n := bufferedVoteCount(engine, beyond); n != 0 {
			t.Fatalf("%d votes for round %d, beyond the window, were buffered", n, beyond)
		}
		engine.startNewRound()
		if got := roundOf(engine); got != beyond {
			t.Fatalf("two of four validators at round %d did not move this validator: it is in round %d", beyond, got)
		}
	})

	t.Run("proposals", func(t *testing.T) {
		// Validator 1 is the proposer of both rounds, so only the window keeps the
		// second one out.
		tv := validatorsWithSchedule(t, 4, map[int]int{edge: 1, beyond: 1})
		engine, _, _ := engineAt(t, tv, current)
		if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(edge, tv.addrs[1], 0x01), edge)); err != nil {
			t.Fatalf("proposal at the edge: %v", err)
		}
		if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(beyond, tv.addrs[1], 0x02), beyond)); err != nil {
			t.Fatalf("proposal beyond the window: %v", err)
		}
		engine.mu.RLock()
		atEdge, atBeyond := len(engine.bufferedProposal[1][edge]), len(engine.bufferedProposal[1][beyond])
		engine.mu.RUnlock()
		if atEdge != 1 {
			t.Fatalf("the proposal at the edge of the window (round %d) was not kept: %d held", edge, atEdge)
		}
		if atBeyond != 0 {
			t.Fatalf("a proposal for round %d, beyond the window, was buffered", beyond)
		}
	})
}

// A validator moves to a later round than the next one only on evidence from
// enough of the voting power, and then to the highest round that evidence
// supports. Each case sends votes from the listed validators (index -> round of
// its vote) to validator 0 sitting at round 1, starts the next round, and reads
// the round it is in. Round 2 is where it lands with no evidence.
func TestOnlyEnoughVotingPowerMovesAValidatorToALaterRound(t *testing.T) {
	cases := []struct {
		name   string
		powers []int64
		votes  map[int]int // validator index -> round of its vote
		want   int
	}{
		{"one of four is not enough", []int64{1, 1, 1, 1}, map[int]int{1: 6}, 2},
		{"two of four are enough", []int64{1, 1, 1, 1}, map[int]int{1: 6, 2: 6}, 6},
		{"the highest supported round, not the lowest and not the highest", []int64{1, 1, 1, 1}, map[int]int{1: 9, 2: 6, 3: 4}, 6},
		{"a claim far above the rest does not raise the jump", []int64{1, 1, 1, 1}, map[int]int{1: 1_000_000, 2: 5, 3: 4}, 5},
		{"a round before the next one moves nothing", []int64{1, 1, 1, 1}, map[int]int{1: 2, 2: 2, 3: 1}, 2},
		{"one of three is not enough", []int64{1, 1, 1}, map[int]int{1: 6}, 2},
		{"two of three are enough", []int64{1, 1, 1}, map[int]int{1: 6, 2: 6}, 6},
		// Two validators cannot make a quorum without each other, so the one that is
		// behind has to follow the other on its word: this is the network a
		// restarted validator has to rejoin.
		{"the other of two", []int64{1, 1}, map[int]int{1: 6}, 6},
		// Exactly a third of the power is not "more than a third": a coalition of a
		// third cannot move a validator that holds two thirds itself.
		{"exactly a third is not enough", []int64{10, 3, 2}, map[int]int{1: 6, 2: 6}, 2},
		{"a small validator cannot move a large one", []int64{10, 4}, map[int]int{1: 6}, 2},
		{"more than a third can", []int64{10, 4, 2}, map[int]int{1: 6, 2: 6}, 6},
		// The other validators are the network the first one has to join: the
		// large one alone is a quorum with it.
		{"a large validator moves a small one", []int64{1, 10}, map[int]int{1: 6}, 6},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tv := newWeightedValidators(t, tc.powers...)
			engine, _, _ := engineAt(t, tv, 1)
			for idx, round := range tc.votes {
				sendVote(t, engine, tv, idx, round)
			}
			engine.startNewRound()
			if got := roundOf(engine); got != tc.want {
				t.Fatalf("validator 0 is in round %d after votes %v, want %d", got, tc.votes, tc.want)
			}
		})
	}
}

// The evidence for a round far beyond the window still counts: two validators of
// four at round 40 move a validator sitting at round 1, at once, though the
// messages that carried it are dropped.
func TestEvidenceBeyondTheWindowMovesAValidatorAtOnce(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 1)
	engine.proposalTimeout, engine.prevoteTimeout, engine.precommitTimeout, engine.commitTimeout = time.Hour, time.Hour, time.Hour, time.Hour

	done := make(chan struct{})
	go func() {
		defer close(done)
		engine.runRound() // returns as soon as the evidence is there, not when its timers end
	}()
	time.Sleep(50 * time.Millisecond)

	const far = 40
	sendVote(t, engine, tv, 1, far)
	select {
	case <-done:
		t.Fatalf("the round loop left its round on one validator's vote")
	case <-time.After(50 * time.Millisecond):
	}
	sendVote(t, engine, tv, 2, far)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("the round loop did not leave its round when two of four validators were seen %d rounds ahead", far-2)
	}

	engine.mu.Lock()
	engine.proposalTimeout, engine.prevoteTimeout, engine.precommitTimeout, engine.commitTimeout = time.Hour, time.Hour, time.Hour, 50*time.Millisecond
	engine.mu.Unlock()
	engine.runRound()
	if got := roundOf(engine); got != far {
		t.Fatalf("validator is in round %d, want %d", got, far)
	}
}

// The number of proposals kept is bounded per validator and in all, identical ones
// are kept once, and a validator loses its oldest first.
func TestBufferedProposalsAreBoundedAndDeduplicated(t *testing.T) {
	tv := newTestValidators(t, 2)
	engine, _, _ := engineAt(t, tv, 1)

	engine.mu.Lock()
	first := newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(3, tv.addrs[1], 0x01), 3)
	for i := 0; i < 5; i++ {
		engine.bufferProposalLocked(first) // the same message again and again
	}
	engine.mu.Unlock()
	if n := bufferedProposalCount(engine); n != 1 {
		t.Fatalf("the same proposal five times is %d proposals, want 1", n)
	}

	engine.mu.Lock()
	for round := 4; round <= 3+maxFutureRounds; round++ {
		engine.bufferProposalLocked(newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(round, tv.addrs[1], byte(round)), round))
	}
	engine.mu.Unlock()
	if n := bufferedProposalCount(engine); n != maxBufferedProposalsPerValidator {
		t.Fatalf("one validator has %d proposals kept, want at most %d", n, maxBufferedProposalsPerValidator)
	}
	engine.mu.RLock()
	_, keptOldest := engine.bufferedProposal[1][3]
	_, keptNewest := engine.bufferedProposal[1][3+maxFutureRounds]
	engine.mu.RUnlock()
	if keptOldest || !keptNewest {
		t.Fatalf("the oldest proposal must be the one dropped: oldest kept=%v newest kept=%v", keptOldest, keptNewest)
	}

	// Many validators cannot exceed the total either.
	many := newTestValidators(t, 3*maxBufferedProposals)
	engine2, _, _ := engineAt(t, many, 1)
	engine2.mu.Lock()
	for i, key := range many.keys {
		for j := 0; j < 3; j++ {
			round := 2 + (i+j)%maxFutureRounds
			engine2.bufferProposalLocked(newSignedProposal(t, key, many.addrs[i], goodBlock(round, many.addrs[i], byte(j)), round))
		}
	}
	engine2.mu.Unlock()
	if n := bufferedProposalCount(engine2); n > maxBufferedProposals {
		t.Fatalf("%d proposals kept, want at most %d", n, maxBufferedProposals)
	}
}

// A validator has one prevote and one precommit per round that count: a second
// vote of a type for a round is not kept, and the votes kept per validator are
// bounded.
func TestBufferedVotesAreBoundedAndDeduplicated(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 1)

	hashA, hashB := sha256Sum([]byte("a")), sha256Sum([]byte("b"))
	for i := 0; i < 4; i++ {
		_ = engine.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hashA, 3, 1))
	}
	_ = engine.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hashB, 3, 1)) // a conflicting one
	_ = engine.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], Precommit, hashA, 3, 1))
	if n := bufferedVoteCount(engine, 3); n != 2 {
		t.Fatalf("a prevote and a precommit for round 3 are two kept votes, not %d", n)
	}

	engine.mu.Lock()
	for round := 2; round < 2+3*maxFutureRounds; round++ {
		for _, vt := range []VoteType{Prevote, Precommit} {
			engine.bufferVoteLocked(signVoteAs(t, tv.keys[2], tv.addrs[2], vt, hashA, round, 1))
		}
	}
	held := 0
	for _, votes := range engine.bufferedVotes[1] {
		for _, v := range votes {
			if bytes.Equal(v.Validator, tv.addrs[2]) {
				held++
			}
		}
	}
	engine.mu.Unlock()
	if held > maxBufferedVotesPerValidator {
		t.Fatalf("one validator has %d votes kept, want at most %d", held, maxBufferedVotesPerValidator)
	}
}

// The memory a validator can make another hold with large proposals is bounded:
// one hundred proposals of about 900 KB, each for a different round, are not one
// hundred times 900 KB.
func TestLargeFutureProposalsDoNotGrowTheHeapWithoutBound(t *testing.T) {
	tv := newTestValidators(t, 2)
	engine, _, _ := engineAt(t, tv, 1)
	payload := make([]byte, 900*1024)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 100; i++ {
		header := &types.BlockHeader{Height: 1, Validator: tv.addrs[1], PrevHash: append([]byte(nil), payload...)}
		round := 2 + i
		if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], types.NewBlock(header, nil), round)); err != nil {
			t.Fatalf("proposal %d: %v", i, err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if n := bufferedProposalCount(engine); n > maxBufferedProposalsPerValidator {
		t.Fatalf("%d proposals of one validator are kept, want at most %d", n, maxBufferedProposalsPerValidator)
	}
	// Two proposals of 900 KB are kept at most (and the block they hold is
	// referenced once); anything near 100 x 900 KB means the buffer is unbounded.
	if limit := int64(24 << 20); grew > limit {
		t.Fatalf("the heap grew by %d MB holding proposals for future rounds, want under %d MB", grew>>20, limit>>20)
	}
}

// A proposal in the buffer from a validator that is not the round's proposer is
// never replayed, whatever put it there.
func TestABufferedProposalFromAnImpostorIsNotReplayed(t *testing.T) {
	const round = 6
	tv := validatorsWithProposer(t, 3, round, 1) // validator 1 proposes; validator 2 is the impostor
	engine, node, br := engineAt(t, tv, round-1)

	impostorBlock := goodBlock(round, tv.addrs[2], 0x0A)
	proposerBlock := goodBlock(round, tv.addrs[1], 0x0B)
	// The impostor's proposal is put in the buffer directly, ahead of the real one,
	// so that it is only the check at replay that can keep it out.
	engine.mu.Lock()
	engine.bufferProposalLocked(newSignedProposal(t, tv.keys[2], tv.addrs[2], impostorBlock, round))
	engine.mu.Unlock()
	if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], proposerBlock, round)); err != nil {
		t.Fatalf("proposer proposal: %v", err)
	}
	engine.startNewRound()
	engine.replayBufferedMessages(1, round)
	queued := drainProposals(engine)
	if len(queued) != 1 || !bytes.Equal(queued[0].Proposer, tv.addrs[1]) {
		t.Fatalf("the replay queued %d proposals, want only the round proposer's", len(queued))
	}

	// And the round loop votes for the proposer's block, having validated nothing else.
	engine.proposalCh <- queued[0]
	engine.mu.Lock()
	engine.currentState.Round = round - 1
	engine.mu.Unlock()
	runOneRound(engine)
	votes := br.votes(tv.addrs[0])
	if len(votes) != 1 || !bytes.Equal(votes[0].Vote.BlockHash, headerHash(t, proposerBlock)) {
		t.Fatalf("expected one prevote for the proposer's block, got %s", describeVotes(votes))
	}
	impostorHash := headerHash(t, impostorBlock)
	for _, validated := range node.validatedHashes() {
		if bytes.Equal(validated, impostorHash) {
			t.Fatalf("a buffered proposal from a validator that is not the round's proposer was validated")
		}
	}
}

// drainProposals empties the engine's proposal queue.
func drainProposals(e *Engine) []*SignedProposal {
	var out []*SignedProposal
	for {
		select {
		case p := <-e.proposalCh:
			out = append(out, p)
		default:
			return out
		}
	}
}

// More than the size of the proposal queue in proposals from a validator that is
// not the round's proposer, sent ahead of the real one, must not push the real one
// out: for a round not yet reached (the buffer and its replay) ...
func TestManyImpostorProposalsCannotCrowdOutTheRealProposerForAFutureRound(t *testing.T) {
	const round = 6
	tv := validatorsWithProposer(t, 3, round, 1)
	engine, _, br := engineAt(t, tv, round-1)

	for i := 0; i < 3*defaultProposalQueueSize; i++ {
		if err := engine.HandleProposal(newSignedProposal(t, tv.keys[2], tv.addrs[2], goodBlock(round, tv.addrs[2], byte(i)), round)); err != nil {
			t.Fatalf("impostor proposal %d: %v", i, err)
		}
	}
	proposerBlock := goodBlock(round, tv.addrs[1], 0xEE)
	if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], proposerBlock, round)); err != nil {
		t.Fatalf("proposer proposal: %v", err)
	}
	runOneRound(engine)

	votes := br.votes(tv.addrs[0])
	if len(votes) != 1 || !bytes.Equal(votes[0].Vote.BlockHash, headerHash(t, proposerBlock)) {
		t.Fatalf("the real proposer's proposal was crowded out: got %s", describeVotes(votes))
	}
}

// ... and for the round the validator is in (the queue the round loop reads).
func TestManyImpostorProposalsCannotCrowdOutTheRealProposerForTheCurrentRound(t *testing.T) {
	const round = 6
	tv := validatorsWithProposer(t, 3, round, 1)
	engine, _, br := engineAt(t, tv, round-1)
	engine.startNewRound() // round 6 is now the current one

	for i := 0; i < 3*defaultProposalQueueSize; i++ {
		if err := engine.HandleProposal(newSignedProposal(t, tv.keys[2], tv.addrs[2], goodBlock(round, tv.addrs[2], byte(i)), round)); err != nil {
			t.Fatalf("impostor proposal %d: %v", i, err)
		}
	}
	proposerBlock := goodBlock(round, tv.addrs[1], 0xEE)
	if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], proposerBlock, round)); err != nil {
		t.Fatalf("the real proposer's proposal was refused: %v", err)
	}
	// The round is the one the validator is in, so run it without starting a new one.
	engine.mu.Lock()
	engine.currentState.Round = round - 1
	engine.mu.Unlock()
	runOneRound(engine)

	votes := br.votes(tv.addrs[0])
	if len(votes) != 1 || !bytes.Equal(votes[0].Vote.BlockHash, headerHash(t, proposerBlock)) {
		t.Fatalf("the real proposer's proposal was crowded out: got %s", describeVotes(votes))
	}
}

// A proposal for the round the validator is in, queued before the round loop
// ran, is not lost when the queue is full of proposals for rounds already over.
func TestTheProposersProposalIsQueuedWhenTheQueueHoldsOnlyStaleOnes(t *testing.T) {
	const round = 4
	tv := validatorsWithProposer(t, 2, round, 1)
	engine, _, _ := engineAt(t, tv, round)
	for i := 0; i < defaultProposalQueueSize; i++ {
		engine.proposalCh <- newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(round-1, tv.addrs[1], byte(i)), round-1)
	}
	real := newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(round, tv.addrs[1], 0x77), round)
	if err := engine.HandleProposal(real); err != nil {
		t.Fatalf("the proposal for the current round was refused with a queue of stale ones: %v", err)
	}
	found := false
	for len(engine.proposalCh) > 0 {
		if got := <-engine.proposalCh; got == real {
			found = true
		}
	}
	if !found {
		t.Fatalf("the proposal for the current round is not on the queue")
	}
}

// The warning about dropped messages is rate limited: however many messages are
// dropped in a short time, one warning is logged, and it says how many it stands
// for.
func TestDroppedMessagesLogOneRateLimitedWarning(t *testing.T) {
	handler := &captureHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(previous)

	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 1)
	for i := 0; i < 200; i++ {
		sendVote(t, engine, tv, 1, 1+maxFutureRounds+1+i) // one validator alone moves nobody
	}

	warnings := handler.warnings("consensus_message_dropped")
	if len(warnings) != 1 {
		t.Fatalf("200 dropped votes logged %d warnings, want exactly 1", len(warnings))
	}
}

// captureHandler is a slog.Handler that keeps the records it is given.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// warnings returns the warning records whose "event" attribute is event.
func (h *captureHandler) warnings(event string) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Level != slog.LevelWarn {
			continue
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "event" && a.Value.String() == event {
				out = append(out, r)
				return false
			}
			return true
		})
	}
	return out
}

// validatorsWithSchedule returns n validators for which the proposer of each round
// in want is the validator with the index it maps to (the proposer of a round
// depends on the order of the addresses, so keys are drawn until the draw fits).
func validatorsWithSchedule(t *testing.T, n int, want map[int]int) *testValidators {
	t.Helper()
	for attempt := 0; attempt < 5000; attempt++ {
		tv := newTestValidators(t, n)
		probe := NewEngine(&signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}, tv.keys[0], &captureBroadcaster{})
		fits := true
		for round, idx := range want {
			if !bytes.Equal(probe.selectProposer(round), tv.addrs[idx]) {
				fits = false
				break
			}
		}
		if fits {
			return tv
		}
	}
	t.Fatalf("no draw of %d validators fits the proposer schedule %v", n, want)
	return nil
}
