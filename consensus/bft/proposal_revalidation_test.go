package bft

// The work a validator does for a round's proposals is bounded.
//
// A signed proposal is public, so the round loop can be handed the same one more
// than once. Validating a block executes all of it with the node's state locked,
// and a second validation of the same signed content cannot give another answer:
// the loop validates a proposal once, and only a few different ones from a signer
// in a round. Likewise a proof of a polka holds one vote for each validator at
// most, and a longer one is refused before any signature in it is checked. These
// tests drive the real round loop and the real proof check.

import (
	"bytes"
	"fmt"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
)

// deliverProposals hands each of the proposals to the engine the way the network
// does, in order, and fails the test if one is refused.
func deliverProposals(t *testing.T, engine *Engine, proposals ...*SignedProposal) {
	t.Helper()
	for i, sp := range proposals {
		if err := engine.HandleProposal(sp); err != nil {
			t.Fatalf("proposal %d: %v", i, err)
		}
	}
}

// runRoundDelivering runs the engine's real round loop, as runOneRound does, and
// hands it the proposals once the round has begun, the way a peer's messages reach a
// validator that is in the round already. (Before that they are kept for replay, and
// the buffer holds no more than a couple of a validator's, with no repeats.)
func runRoundDelivering(t *testing.T, engine *Engine, round int, proposals ...*SignedProposal) {
	t.Helper()
	engine.proposalTimeout, engine.prevoteTimeout, engine.precommitTimeout = time.Hour, time.Hour, time.Hour
	engine.commitTimeout = 250 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		engine.runRound()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		engine.mu.RLock()
		current := engine.currentState.Round
		engine.mu.RUnlock()
		if current == round {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine did not reach round %d", round)
		}
		time.Sleep(time.Millisecond)
	}
	deliverProposals(t, engine, proposals...)
	<-done
}

// The same proposal reaching the round loop several times is validated once and
// answered once, whether the block is good or bad.
func TestAProposalQueuedSeveralTimesIsValidatedOnce(t *testing.T) {
	const round, copies = 4, 12
	cases := []struct {
		name    string
		block   func(tv *testValidators) *types.Block
		wantNil bool // the one prevote is for nil
	}{
		{name: "a good block", block: func(tv *testValidators) *types.Block { return goodBlock(round, tv.addrs[1], 0x01) }},
		{name: "a bad block", block: func(tv *testValidators) *types.Block { return badBlock(round, tv.addrs[1], 0x01) }, wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tv := validatorsWithProposer(t, 2, round, 1) // validator 1 proposes the round; validator 0 only votes
			engine, node, br := newVoter(t, tv)
			engine.mu.Lock()
			engine.currentState = State{Height: 1, Round: round - 1}
			engine.mu.Unlock()

			block := tc.block(tv)
			signed := newSignedProposal(t, tv.keys[1], tv.addrs[1], block, round)
			copyList := make([]*SignedProposal, copies)
			for i := range copyList {
				copyList[i] = signed
			}
			runRoundDelivering(t, engine, round, copyList...)

			if got := len(node.validatedHashes()); got != 1 {
				t.Fatalf("the block of a proposal queued %d times was validated %d times, want once", copies, got)
			}
			votes := br.votes(tv.addrs[0])
			if got := countType(votes, Prevote); got != 1 || len(votes) != 1 {
				t.Fatalf("expected exactly one prevote, got %s", describeVotes(votes))
			}
			wantHash := headerHash(t, block)
			if tc.wantNil {
				wantHash = nil
			}
			if !bytes.Equal(votes[0].Vote.BlockHash, wantHash) {
				t.Fatalf("the prevote is for %x, want %x", votes[0].Vote.BlockHash, wantHash)
			}
			requireNoSlashableProof(t, votes, tv.addrs[0])
		})
	}
}

// Different proposals of the round's proposer are each validated, as before, up to
// the limit, and none beyond it; the answer is still the first one's.
func TestDifferentProposalsOfOneSignerAreValidatedUpToALimit(t *testing.T) {
	const round = 4
	for _, sent := range []int{2, maxProposalsValidatedPerSigner, maxProposalsValidatedPerSigner + 1, maxProposalsValidatedPerSigner + 6} {
		t.Run(fmt.Sprintf("%d different proposals", sent), func(t *testing.T) {
			tv := validatorsWithProposer(t, 2, round, 1)
			engine, node, br := newVoter(t, tv)
			engine.mu.Lock()
			engine.currentState = State{Height: 1, Round: round - 1}
			engine.mu.Unlock()

			var blocks []*types.Block
			var proposals []*SignedProposal
			for i := 0; i < sent; i++ {
				block := goodBlock(round, tv.addrs[1], byte(i+1))
				blocks = append(blocks, block)
				proposals = append(proposals, newSignedProposal(t, tv.keys[1], tv.addrs[1], block, round))
			}
			runRoundDelivering(t, engine, round, proposals...)

			want := sent
			if want > maxProposalsValidatedPerSigner {
				want = maxProposalsValidatedPerSigner
			}
			validated := node.validatedHashes()
			if len(validated) != want {
				t.Fatalf("%d different proposals: %d validated, want %d", sent, len(validated), want)
			}
			for i, hash := range validated {
				if !bytes.Equal(hash, headerHash(t, blocks[i])) {
					t.Fatalf("validation %d was of %x, want the proposals in the order they came (%x)", i, hash, headerHash(t, blocks[i]))
				}
			}
			votes := br.votes(tv.addrs[0])
			if len(votes) != 1 || !bytes.Equal(votes[0].Vote.BlockHash, headerHash(t, blocks[0])) {
				t.Fatalf("the one prevote must be for the first proposal, got %s", describeVotes(votes))
			}
		})
	}
}

// A proposal the loop did not validate because it had validated it already does
// not stand in the way of the next, different one.
func TestARepeatedProposalDoesNotHoldUpTheNextDifferentOne(t *testing.T) {
	const round = 4
	tv := validatorsWithProposer(t, 2, round, 1)
	engine, node, br := newVoter(t, tv)
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: round - 1}
	engine.mu.Unlock()

	bad := newSignedProposal(t, tv.keys[1], tv.addrs[1], badBlock(round, tv.addrs[1], 0x01), round)
	good := newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(round, tv.addrs[1], 0x02), round)
	runRoundDelivering(t, engine, round, bad, bad, bad, good, bad)

	if got := len(node.validatedHashes()); got != 2 {
		t.Fatalf("the bad and the good proposal were each to be validated once, %d validations", got)
	}
	// The bad one drew the validator's one vote of the round, as it always did.
	votes := br.votes(tv.addrs[0])
	if len(votes) != 1 || len(votes[0].Vote.BlockHash) != 0 {
		t.Fatalf("expected the one prevote to be for nil, got %s", describeVotes(votes))
	}
}

// proposalValidations on its own: signers are kept apart, and what is compared is
// what was signed, not how the signature is written.
func TestProposalValidationsTellProposalsAndSignersApart(t *testing.T) {
	const round = 4
	tv := newTestValidators(t, 2)
	var v proposalValidations

	first := newSignedProposal(t, tv.keys[0], tv.addrs[0], goodBlock(round, tv.addrs[0], 0x01), round)
	if reason := v.admit(first); reason != "" {
		t.Fatalf("a first proposal is validated, got %q", reason)
	}
	if reason := v.admit(first); reason != dropRepeatedProposal {
		t.Fatalf("the same proposal a second time: %q, want %q", reason, dropRepeatedProposal)
	}

	// The same content with the signature written differently is the same proposal.
	reencoded := *first
	sig := *first.Signature
	sig.Signature = append(append([]byte(nil), sig.Signature...), 0x00)
	reencoded.Signature = &sig
	if reason := v.admit(&reencoded); reason != dropRepeatedProposal {
		t.Fatalf("the same proposal with another signature encoding: %q, want %q", reason, dropRepeatedProposal)
	}

	// The same content from another signer is another proposal.
	other := *first
	other.Proposer = tv.addrs[1]
	if reason := v.admit(&other); reason != "" {
		t.Fatalf("the same block from another signer is validated, got %q", reason)
	}

	// One signer fills its own allowance and no one else's.
	for i := 1; i < maxProposalsValidatedPerSigner; i++ {
		sp := newSignedProposal(t, tv.keys[0], tv.addrs[0], goodBlock(round, tv.addrs[0], byte(0x10+i)), round)
		if reason := v.admit(sp); reason != "" {
			t.Fatalf("proposal %d of the signer is validated, got %q", i+1, reason)
		}
	}
	over := newSignedProposal(t, tv.keys[0], tv.addrs[0], goodBlock(round, tv.addrs[0], 0x40), round)
	if reason := v.admit(over); reason != dropTooManyProposals {
		t.Fatalf("a proposal past the limit: %q, want %q", reason, dropTooManyProposals)
	}
	if reason := v.admit(first); reason != dropRepeatedProposal {
		t.Fatalf("a repeat past the limit is still a repeat, got %q", reason)
	}
	next := newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(round, tv.addrs[1], 0x41), round)
	if reason := v.admit(next); reason != "" {
		t.Fatalf("another signer has an allowance of its own, got %q", reason)
	}
}

// pollOfValidators signs a prevote for hash at (round, height) with each of the
// first n validators.
func pollOfValidators(t *testing.T, tv *testValidators, n int, hash []byte, round int, height uint64) []*SignedVote {
	t.Helper()
	var proof []*SignedVote
	for i := 0; i < n; i++ {
		proof = append(proof, signVoteAs(t, tv.keys[i], tv.addrs[i], Prevote, hash, round, height))
	}
	return proof
}

// A proof is one vote for each validator at most. One that is longer is refused,
// even when the votes in it would make a polka, and one of exactly the size of the
// validator set still verifies.
func TestAPolkaProofLongerThanTheValidatorSetIsRefused(t *testing.T) {
	const polkaRound, height = 2, 1
	tv := newTestValidators(t, 4)
	total := big.NewInt(int64(len(tv.addrs)))
	hash := headerHash(t, candidateBlock(height, polkaRound, tv.addrs[0], 0x55))

	three := pollOfValidators(t, tv, 3, hash, polkaRound, height)
	four := pollOfValidators(t, tv, 4, hash, polkaRound, height)
	if !verifyPolkaProof(three, polkaRound, hash, height, tv.set, total) {
		t.Fatalf("test setup: 3 of 4 validators is a polka")
	}
	if !verifyPolkaProof(four, polkaRound, hash, height, tv.set, total) {
		t.Fatalf("a proof with a vote of each of the validators, as many as the set, must verify")
	}

	cases := []struct {
		name  string
		proof []*SignedVote
	}{
		{"a polka and a repeat of a vote of it", append(append([]*SignedVote(nil), four...), four[0])},
		{"a polka and an entry that is not signed", append(append([]*SignedVote(nil), four...), &SignedVote{Vote: four[0].Vote, Validator: tv.addrs[0]})},
		{"a polka and empty entries", append(append([]*SignedVote(nil), four...), nil, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.proof) <= len(tv.set) {
				t.Fatalf("test setup: the proof has %d entries, not more than the %d validators", len(tc.proof), len(tv.set))
			}
			if verifyPolkaProof(tc.proof, polkaRound, hash, height, tv.set, total) {
				t.Fatalf("a proof of %d entries for %d validators was accepted", len(tc.proof), len(tv.set))
			}
		})
	}
}

// What the length check saves is the signature checks of the entries: a proof
// of well-formed votes none of which verifies costs one check each when it is
// worked through, and nothing once it is refused for its length.
func TestAnOverlongPolkaProofIsRefusedWithoutCheckingItsSignatures(t *testing.T) {
	const polkaRound, height, entries = 2, 1, 1000
	tv := newTestValidators(t, 4)
	total := big.NewInt(int64(len(tv.addrs)))
	hash := headerHash(t, candidateBlock(height, polkaRound, tv.addrs[0], 0x56))
	otherHash := headerHash(t, candidateBlock(height, polkaRound, tv.addrs[0], 0x57))

	// A vote whose signature is valid but for another block does not verify for this one.
	proof := make([]*SignedVote, 0, entries)
	for i := 0; i < entries; i++ {
		sv := signVoteAs(t, tv.keys[i%len(tv.keys)], tv.addrs[i%len(tv.keys)], Prevote, otherHash, polkaRound, height)
		sv.Vote = &Vote{BlockHash: hash, Round: polkaRound, Type: Prevote, Height: height}
		proof = append(proof, sv)
	}

	start := time.Now()
	counted, _ := countedPolkaVotes(proof, polkaRound, hash, height, tv.set)
	checked := time.Since(start)
	if len(counted) != 0 {
		t.Fatalf("test setup: %d of the entries verified", len(counted))
	}

	start = time.Now()
	ok := verifyPolkaProof(proof, polkaRound, hash, height, tv.set, total)
	refused := time.Since(start)
	if ok {
		t.Fatalf("a proof of votes that do not verify was accepted")
	}
	if refused > checked/10 {
		t.Fatalf("refusing a proof of %d entries took %v, against %v to check its signatures: the signatures were checked", entries, refused, checked)
	}
}

// The proof check is what a validator's prevote for a re-proposal rests on, so the
// round loop is driven end to end: a re-proposal whose proof is longer than the
// validator set draws a prevote for nil, and one whose proof is not draws a prevote
// for the block.
func TestAReProposalWithAnOverlongProofIsNotPrevoted(t *testing.T) {
	const round, polkaRound = 5, 2
	cases := []struct {
		name     string
		extra    int  // repeats of a vote added to the proof
		wantVote bool // the prevote is for the block
	}{
		{name: "a proof of one vote for each validator", extra: 0, wantVote: true},
		{name: "a proof with a repeated vote added", extra: 1, wantVote: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tv := validatorsWithProposer(t, 3, round, 1) // validator 1 proposes round 5
			engine, _, br := newVoter(t, tv)
			engine.mu.Lock()
			engine.currentState = State{Height: 1, Round: round - 1}
			engine.mu.Unlock()

			block := candidateBlock(1, polkaRound, tv.addrs[0], 0x21)
			hash := headerHash(t, block)
			proof := pollOfValidators(t, tv, 3, hash, polkaRound, 1)
			for i := 0; i < tc.extra; i++ {
				proof = append(proof, proof[0])
			}
			reproposal := &Proposal{Block: block, Round: round, ValidRound: polkaRound, ValidRoundProof: proof}
			sig, err := ethSign(sha256Sum(reproposal.bytes()), tv.keys[1])
			if err != nil {
				t.Fatalf("sign re-proposal: %v", err)
			}
			signed := &SignedProposal{Proposal: reproposal, Proposer: tv.addrs[1], Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig}}
			deliverProposals(t, engine, signed)
			runOneRound(engine)

			votes := br.votes(tv.addrs[0])
			if len(votes) != 1 || votes[0].Vote.Type != Prevote || votes[0].Vote.Round != round {
				t.Fatalf("expected one prevote in round %d, got %s", round, describeVotes(votes))
			}
			wantHash := hash
			if !tc.wantVote {
				wantHash = nil
			}
			if !bytes.Equal(votes[0].Vote.BlockHash, wantHash) {
				t.Fatalf("the prevote is for %x, want %x", votes[0].Vote.BlockHash, wantHash)
			}
		})
	}
}
