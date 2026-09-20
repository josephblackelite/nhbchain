package bft

// Tests for passing a polka on (round_sync.go, relayValidBlock and learnValidBlock)
// and for keeping a vote that arrives ahead of its proposal.
//
// A validator that locked on a block holds the proof that lets anyone accept it
// again -- the signed prevotes of the polka -- but used to show it only in a
// re-proposal of its own, so a validator that knew nothing of the lock (restarted,
// or its vote arrived a round late) proposed new blocks that the locked one refused
// until the locked validator's own proposer turn came. Now the locked validator
// passes the polka on, in an ordinary proposal message that a receiver only learns
// from.

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/p2p"
)

// lockOn makes the engine (validator 0 of a two validator set) lock on block at
// round: it prevotes it, and the other validator's prevote arrives. The engine is
// then in laterRound, having moved on, with no proposal.
func lockOn(t *testing.T, e *Engine, tv *testValidators, block *types.Block, round, laterRound int) []byte {
	t.Helper()
	hash := headerHash(t, block)
	e.mu.Lock()
	e.currentState.Round = round
	e.mu.Unlock()
	if !e.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: block, Round: round, ValidRound: -1}, Proposer: tv.addrs[1]}) {
		t.Fatalf("test setup: the proposal was not accepted")
	}
	e.prevote()
	e.addVoteIfRelevant(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hash, round, 1))
	if _, _, locked := e.Status(); locked != round {
		t.Fatalf("test setup: the engine is not locked at round %d (locked round %d)", round, locked)
	}
	e.mu.Lock()
	e.currentState.Round = laterRound
	e.activeProposal = nil
	e.prevoteSent = false
	e.mu.Unlock()
	return hash
}

// relaysBy decodes the proposals in msgs that signer signed.
func relaysBy(msgs []*p2p.Message, signer []byte) []*SignedProposal {
	var out []*SignedProposal
	for _, m := range msgs {
		if m.Type != p2p.MsgTypeProposal {
			continue
		}
		var sp SignedProposal
		if err := json.Unmarshal(m.Payload, &sp); err != nil || sp.Proposal == nil {
			continue
		}
		if bytes.Equal(sp.Proposer, signer) {
			out = append(out, &sp)
		}
	}
	return out
}

// A validator that holds a polka passes it on once in each round in which it
// hears from a validator that is behind, and a validator that holds none says
// nothing.
func TestAValidatorHoldingAPolkaPassesItOnToAValidatorThatIsBehind(t *testing.T) {
	tv := newTestValidators(t, 2)
	engine, _, br := engineAt(t, tv, 0)
	blockX := candidateBlock(1, 7, tv.addrs[0], 0x58)
	hashX := lockOn(t, engine, tv, blockX, 7, 12)
	before := len(relaysBy(br.messages(), tv.addrs[0]))

	// The other validator's vote for round 3 is stale for a validator in round 12.
	if err := engine.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, sha256Sum([]byte("y")), 3, 1)); err != nil {
		t.Fatalf("stale vote: %v", err)
	}
	relays := relaysBy(br.messages(), tv.addrs[0])
	if len(relays) != before+1 {
		t.Fatalf("expected the polka to be passed on once, %d proposals were sent", len(relays)-before)
	}
	relay := relays[len(relays)-1]
	if relay.Proposal.Round != 12 || relay.Proposal.ValidRound != 7 {
		t.Fatalf("the relay is for round %d with valid round %d, want round 12 and valid round 7", relay.Proposal.Round, relay.Proposal.ValidRound)
	}
	if got := headerHash(t, relay.Proposal.Block); !bytes.Equal(got, hashX) {
		t.Fatalf("the relay carries block %x, want the locked block %x", got, hashX)
	}
	if len(relay.Proposal.ValidRoundProof) < 2 {
		t.Fatalf("the relay carries %d prevotes, want the whole polka", len(relay.Proposal.ValidRoundProof))
	}
	if err := engine.verifySignedProposal(relay); err != nil {
		t.Fatalf("the relay is not a well-formed signed proposal: %v", err)
	}

	// Once a round, however many messages ask.
	for i := 0; i < 5; i++ {
		_ = engine.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, sha256Sum([]byte("y")), 4, 1))
	}
	if n := len(relaysBy(br.messages(), tv.addrs[0])); n != before+1 {
		t.Fatalf("the polka was passed on %d times in one round", n-before)
	}
	// In the next round again.
	engine.mu.Lock()
	engine.currentState.Round = 13
	engine.mu.Unlock()
	_ = engine.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, sha256Sum([]byte("y")), 4, 1))
	if n := len(relaysBy(br.messages(), tv.addrs[0])); n != before+2 {
		t.Fatalf("the polka was not passed on in the next round (%d relays)", n-before)
	}

	// A validator with no polka has nothing to pass on.
	none, _, brNone := engineAt(t, tv, 12)
	_ = none.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, sha256Sum([]byte("y")), 3, 1))
	if n := len(relaysBy(brNone.messages(), tv.addrs[0])); n != 0 {
		t.Fatalf("a validator with no polka sent %d proposals", n)
	}
}

// The validator that holds the polka also passes it on when it refuses a proposal
// because of its lock, in the round of that proposal.
func TestALockedValidatorPassesItsPolkaOnWhenItRefusesAProposal(t *testing.T) {
	const round = 12
	tv := validatorsWithProposer(t, 2, round, 1) // validator 1 proposes round 12
	engine, _, br := engineAt(t, tv, 0)
	blockX := candidateBlock(1, 7, tv.addrs[0], 0x58)
	hashX := lockOn(t, engine, tv, blockX, 7, round-1)

	fresh := goodBlock(round, tv.addrs[1], 0x99)
	if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], fresh, round)); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	runOneRound(engine)

	votes := br.votes(tv.addrs[0])
	refused := false
	for _, v := range votes {
		if v.Vote.Round == round && v.Vote.Type == Prevote && len(v.Vote.BlockHash) == 0 {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("test setup: the locked validator did not refuse the proposal: %s", describeVotes(votes))
	}
	relays := relaysBy(br.messages(), tv.addrs[0])
	found := false
	for _, r := range relays {
		if r.Proposal.Round == round && r.Proposal.ValidRound == 7 && bytes.Equal(headerHash(t, r.Proposal.Block), hashX) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the locked validator refused a proposal without passing its polka on (%d proposals of its own were sent)", len(relays))
	}
}

// A validator that knows nothing of the lock learns the block and the polka from
// the relay, re-proposes the block when it is the proposer, and the locked validator
// accepts it: the height commits without the locked validator's own turn.
func TestAValidatorThatLearnsAPolkaReProposesTheBlockAndTheLockedValidatorAcceptsIt(t *testing.T) {
	const round = 12
	// The locked validator (0) is not the proposer of round 12, the other one is.
	tv := validatorsWithProposer(t, 2, round, 1)
	locked, _, brLocked := engineAt(t, tv, 0)
	blockX := candidateBlock(1, 7, tv.addrs[0], 0x58)
	hashX := lockOn(t, locked, tv, blockX, 7, round)
	_ = locked.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, sha256Sum([]byte("y")), 3, 1))
	relays := relaysBy(brLocked.messages(), tv.addrs[0])
	if len(relays) == 0 {
		t.Fatalf("test setup: no relay")
	}
	relay := relays[len(relays)-1]

	// The other validator, restarted: it knows nothing of the polka, and is at round 8.
	node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[1]}
	brOther := &captureBroadcaster{}
	other := NewEngine(node, tv.keys[1], brOther)
	other.mu.Lock()
	other.currentState = State{Height: 1, Round: 8}
	other.mu.Unlock()
	if err := other.HandleProposal(relay); err != nil {
		t.Fatalf("relay: %v", err)
	}
	other.mu.RLock()
	learnt, learntRound := other.validBlock, other.validRound
	other.mu.RUnlock()
	if learnt == nil || !bytes.Equal(headerHash(t, learnt), hashX) || learntRound != 7 {
		t.Fatalf("the relay did not teach the block and the polka: valid block %v, valid round %d", learnt, learntRound)
	}
	if got := roundOf(other); got != 8 {
		t.Fatalf("test setup: the round of the relay's sender should not have moved a validator that has to be told (round %d)", got)
	}

	// It moves to the sender's round (the only other validator of two), and as the
	// proposer of it re-proposes the locked block with the proof.
	other.mu.Lock()
	other.currentState.Round = round - 1
	other.mu.Unlock()
	other.startNewRound()
	if got := roundOf(other); got != round {
		t.Fatalf("the other validator is in round %d, want %d", got, round)
	}
	if err := other.propose(); err != nil {
		t.Fatalf("propose: %v", err)
	}
	proposals := relaysBy(brOther.messages(), tv.addrs[1])
	if len(proposals) != 1 {
		t.Fatalf("expected one proposal from the other validator, got %d", len(proposals))
	}
	reProposal := proposals[0]
	if !bytes.Equal(headerHash(t, reProposal.Proposal.Block), hashX) || reProposal.Proposal.ValidRound != 7 || len(reProposal.Proposal.ValidRoundProof) < 2 {
		t.Fatalf("the other validator proposed a block that is not the locked one, or without the proof of it")
	}

	// The locked validator accepts it: it prevotes X, not nil.
	if err := locked.HandleProposal(reProposal); err != nil {
		t.Fatalf("re-proposal: %v", err)
	}
	locked.mu.Lock()
	locked.currentState.Round = round - 1
	locked.mu.Unlock()
	runOneRound(locked)
	var prevotedX bool
	for _, v := range brLocked.votes(tv.addrs[0]) {
		if v.Vote.Round == round && v.Vote.Type == Prevote && bytes.Equal(v.Vote.BlockHash, hashX) {
			prevotedX = true
		}
	}
	if !prevotedX {
		t.Fatalf("the locked validator did not prevote the block it is locked on when it was re-proposed: %s", describeVotes(brLocked.votes(tv.addrs[0])))
	}
}

// A relay is only learnt from when its proof is a real polka for the block it
// carries, at the round it names, from more than two thirds of the power, and is
// for a later round than the polka already held.
func TestAPolkaIsLearntOnlyFromAGoodProof(t *testing.T) {
	tv := newTestValidators(t, 3)
	hashOf := func(b *types.Block) []byte { return headerHash(t, b) }
	block := candidateBlock(1, 4, tv.addrs[0], 0x41)
	other := candidateBlock(1, 4, tv.addrs[0], 0x42)
	prevote := func(idx int, b *types.Block, round int) *SignedVote {
		return signVoteAs(t, tv.keys[idx], tv.addrs[idx], Prevote, hashOf(b), round, 1)
	}
	signed := func(prop *Proposal) *SignedProposal {
		sig, err := ethSign(sha256Sum(prop.bytes()), tv.keys[0])
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return &SignedProposal{Proposal: prop, Proposer: tv.addrs[0], Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig}}
	}
	all := []*SignedVote{prevote(0, block, 4), prevote(1, block, 4), prevote(2, block, 4)}

	cases := []struct {
		name  string
		prop  *Proposal
		learn bool
	}{
		{"a full polka", &Proposal{Block: block, Round: 9, ValidRound: 4, ValidRoundProof: all}, true},
		{"one vote of three", &Proposal{Block: block, Round: 9, ValidRound: 4, ValidRoundProof: all[:1]}, false},
		{"exactly two thirds", &Proposal{Block: block, Round: 9, ValidRound: 4, ValidRoundProof: all[:2]}, false},
		{"votes for another block", &Proposal{Block: other, Round: 9, ValidRound: 4, ValidRoundProof: all}, false},
		{"votes of another round", &Proposal{Block: block, Round: 9, ValidRound: 5, ValidRoundProof: all}, false},
		{"a polka of the round of the proposal", &Proposal{Block: block, Round: 4, ValidRound: 4, ValidRoundProof: all}, false},
		{"a proof of the same vote repeated", &Proposal{Block: block, Round: 9, ValidRound: 4, ValidRoundProof: []*SignedVote{all[0], all[0], all[0], all[0], all[0]}}, false},
		{"no proof", &Proposal{Block: block, Round: 9, ValidRound: 4}, false},
		{"no valid round", &Proposal{Block: block, Round: 9, ValidRound: -1, ValidRoundProof: all}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			engine, _, _ := engineAt(t, tv, 8)
			if err := engine.HandleProposal(signed(tc.prop)); err != nil {
				t.Fatalf("proposal: %v", err)
			}
			engine.mu.RLock()
			got := engine.validBlock != nil
			engine.mu.RUnlock()
			if got != tc.learn {
				t.Fatalf("learnt=%v, want %v", got, tc.learn)
			}
		})
	}

	// A polka for an earlier round than the one held is not learnt.
	engine, _, _ := engineAt(t, tv, 8)
	newer := &Proposal{Block: block, Round: 9, ValidRound: 4, ValidRoundProof: all}
	if err := engine.HandleProposal(signed(newer)); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	older := candidateBlock(1, 2, tv.addrs[0], 0x43)
	olderProof := []*SignedVote{prevote(0, older, 2), prevote(1, older, 2), prevote(2, older, 2)}
	if err := engine.HandleProposal(signed(&Proposal{Block: older, Round: 9, ValidRound: 2, ValidRoundProof: olderProof})); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	engine.mu.RLock()
	round, held := engine.validRound, engine.validBlock
	engine.mu.RUnlock()
	if round != 4 || held == nil || !bytes.Equal(hashOf(held), hashOf(block)) {
		t.Fatalf("an older polka replaced the newer one: valid round %d", round)
	}
}

// A relay from a validator that is not the round's proposer is not the round's
// proposal: it does not take its place, cost a validation or draw a vote, and it
// does not lock or unlock anything.
func TestARelayFromAValidatorThatIsNotTheProposerIsOnlyLearntFrom(t *testing.T) {
	const round = 6
	tv := validatorsWithProposer(t, 3, round, 2) // validator 2 proposes; validator 1 relays
	engine, node, br := engineAt(t, tv, round-1)
	engine.startNewRound() // round 6

	block := candidateBlock(1, 2, tv.addrs[1], 0x51)
	hash := headerHash(t, block)
	proof := []*SignedVote{
		signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hash, 2, 1),
		signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hash, 2, 1),
		signVoteAs(t, tv.keys[2], tv.addrs[2], Prevote, hash, 2, 1),
	}
	prop := &Proposal{Block: block, Round: round, ValidRound: 2, ValidRoundProof: proof}
	sig, err := ethSign(sha256Sum(prop.bytes()), tv.keys[1])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	relay := &SignedProposal{Proposal: prop, Proposer: tv.addrs[1], Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig}}
	if err := engine.HandleProposal(relay); err != nil {
		t.Fatalf("relay: %v", err)
	}
	if n := len(engine.proposalCh); n != 0 {
		t.Fatalf("a proposal from a validator that is not the proposer was queued")
	}
	engine.mu.Lock()
	engine.currentState.Round = round - 1
	engine.mu.Unlock()
	runOneRound(engine)
	if votes := br.votes(tv.addrs[0]); len(votes) != 0 {
		t.Fatalf("the relay drew a vote: %s", describeVotes(votes))
	}
	for _, validated := range node.validatedHashes() {
		if bytes.Equal(validated, hash) {
			t.Fatalf("the relay's block was validated as if it were the round's proposal")
		}
	}
	engine.mu.RLock()
	learnt := engine.validBlock != nil
	_, _, locked := engine.Status()
	engine.mu.RUnlock()
	if !learnt || locked != -1 {
		t.Fatalf("expected the block to be learnt as valid and nothing locked: learnt=%v locked round=%d", learnt, locked)
	}
}

// A vote that arrives before the proposal it is for is counted once the proposal is
// accepted, instead of being lost: the round below commits only if it is (two
// validators, so every vote is needed).
func TestAVoteAheadOfItsProposalIsCountedWhenTheProposalArrives(t *testing.T) {
	const round = 4
	tv := validatorsWithProposer(t, 2, round, 1)
	engine, _, br := engineAt(t, tv, round-1)
	block := goodBlock(round, tv.addrs[1], 0x61)
	hash := headerHash(t, block)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runOneRound(engine)
	}()
	time.Sleep(40 * time.Millisecond) // the round is running, with no proposal yet
	if err := engine.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hash, round, 1)); err != nil {
		t.Fatalf("vote: %v", err)
	}
	time.Sleep(40 * time.Millisecond) // the loop has read the vote
	if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], block, round)); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	<-done

	precommitted := false
	for _, v := range br.votes(tv.addrs[0]) {
		if v.Vote.Type == Precommit && v.Vote.Round == round && bytes.Equal(v.Vote.BlockHash, hash) {
			precommitted = true
		}
	}
	if !precommitted {
		t.Fatalf("the prevote that arrived before the proposal was lost: no precommit for the block (%s)", describeVotes(br.votes(tv.addrs[0])))
	}
}
