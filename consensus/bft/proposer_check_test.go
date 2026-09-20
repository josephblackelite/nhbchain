package bft

// Only the round's proposer proposes in it.
//
// HandleProposal accepts a proposal from any validator, so before the check in
// runRound any one validator could send the honest validator a block of its own
// ahead of the real proposer's and draw the honest validator's prevote for it
// (the real proposer's block then found the round's one proposal taken), or send
// it a stream of proposals to validate. These tests drive the real round loop.

import (
	"bytes"
	"errors"
	"testing"

	"nhbchain/core/types"
)

// A validator that is not the proposer and sends its own block first must not
// draw the honest validator's prevote, nor cost it a validation, nor the
// proposer's block its place in the round.
func TestAProposalFromAValidatorThatIsNotTheRoundsProposerIsIgnored(t *testing.T) {
	const round = 6
	tv := validatorsWithProposer(t, 3, round, 1) // validator 1 proposes; validator 2 is the impostor
	engine, node, br := newVoter(t, tv)
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: round - 1}
	engine.mu.Unlock()

	impostorBlock := candidateBlock(1, round, tv.addrs[2], 0x0A)
	proposerBlock := candidateBlock(1, round, tv.addrs[1], 0x0B)
	// The impostor's proposal arrives first.
	if err := engine.HandleProposal(newSignedProposal(t, tv.keys[2], tv.addrs[2], impostorBlock, round)); err != nil {
		t.Fatalf("impostor proposal: %v", err)
	}
	if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], proposerBlock, round)); err != nil {
		t.Fatalf("proposer proposal: %v", err)
	}
	runOneRound(engine)

	votes := br.votes(tv.addrs[0])
	if len(votes) != 1 || votes[0].Vote.Type != Prevote {
		t.Fatalf("expected one prevote, got %s", describeVotes(votes))
	}
	if !bytes.Equal(votes[0].Vote.BlockHash, headerHash(t, proposerBlock)) {
		t.Fatalf("the validator prevoted %x, which is not the round proposer's block %x", votes[0].Vote.BlockHash, headerHash(t, proposerBlock))
	}
	impostorHash := headerHash(t, impostorBlock)
	for _, validated := range node.validatedHashes() {
		if bytes.Equal(validated, impostorHash) {
			t.Fatalf("a proposal from a validator that is not the round's proposer was validated")
		}
	}
}

// A re-proposal legitimately carries the block of an earlier round, authored by
// another validator: the proposer of THIS round signs it, and it is the signer
// that must be the round's proposer, not the block's author.
func TestTheRoundsProposerMayReProposeABlockAnotherValidatorAuthored(t *testing.T) {
	const round, polkaRound = 5, 2
	tv := validatorsWithProposer(t, 2, round, 1) // validator 1 proposes round 5
	engine, _, br := newVoter(t, tv)
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: round - 1}
	engine.mu.Unlock()

	// The block was authored by validator 0 in an earlier round and both
	// validators prevoted it there: a polka, which is the proposer's proof.
	block := candidateBlock(1, polkaRound, tv.addrs[0], 0x21)
	hash := headerHash(t, block)
	proof := []*SignedVote{
		signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hash, polkaRound, 1),
		signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hash, polkaRound, 1),
	}
	reproposal := &Proposal{Block: block, Round: round, ValidRound: polkaRound, ValidRoundProof: proof}
	sig, err := ethSign(sha256Sum(reproposal.bytes()), tv.keys[1])
	if err != nil {
		t.Fatalf("sign re-proposal: %v", err)
	}
	signed := &SignedProposal{Proposal: reproposal, Proposer: tv.addrs[1], Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig}}
	if err := engine.HandleProposal(signed); err != nil {
		t.Fatalf("re-proposal: %v", err)
	}
	runOneRound(engine)

	votes := br.votes(tv.addrs[0])
	if len(votes) != 1 || votes[0].Vote.Type != Prevote || !bytes.Equal(votes[0].Vote.BlockHash, hash) || votes[0].Vote.Round != round {
		t.Fatalf("the re-proposal by the round's proposer must be prevoted in round %d, got %s", round, describeVotes(votes))
	}
}

// signGuardNodeWithoutAccounts cannot look any account up, so no proposer can be
// chosen for a round.
type signGuardNodeWithoutAccounts struct{ *signGuardNode }

func (n signGuardNodeWithoutAccounts) GetAccount([]byte) (*types.Account, error) {
	return nil, errors.New("state unavailable")
}

// When this validator cannot tell who the round's proposer is (it cannot read
// the accounts the choice is made from), it does not refuse proposals for that:
// it behaves as it always did and accepts the first valid proposal from any
// validator, rather than let a state-read failure silence every proposer.
func TestProposalsAreNotDroppedWhenTheProposerCannotBeDetermined(t *testing.T) {
	const round = 3
	tv := newTestValidators(t, 2)
	node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}
	br := &captureBroadcaster{}
	engine := NewEngine(signGuardNodeWithoutAccounts{node}, tv.keys[0], br)
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: round - 1}
	engine.mu.Unlock()
	if got := engine.selectProposer(round); len(got) != 0 {
		t.Fatalf("test setup: no proposer should be determinable, got %x", got)
	}

	block := candidateBlock(1, round, tv.addrs[1], 0x31)
	if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], block, round)); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	runOneRound(engine)

	votes := br.votes(tv.addrs[0])
	if len(votes) != 1 || !bytes.Equal(votes[0].Vote.BlockHash, headerHash(t, block)) {
		t.Fatalf("with no proposer determinable the proposal must still be prevoted, got %s", describeVotes(votes))
	}
}
