package bft

// NHB-AUDIT-C1 regression tests.
//
// The external audit's minimal reproduction: with a 4-validator set and a
// 3-of-4 threshold, a single equivocating validator plus one honest
// validator's own timed-out-and-forgotten precommit was enough for two
// different 3-of-4 quorums to each independently commit a different block
// at the same height, forking the chain -- because startNewRound() wiped
// every round's state (including a just-broadcast precommit) unconditionally
// on every round timeout, with no lock/Proof-of-Lock-Change concept
// anywhere in the engine or the wire protocol.
//
// These tests drive the real production methods (addVoteIfRelevant,
// prevote, precommit, propose, startNewRound, verifySignedProposal,
// lockCompliesLocked) directly, the same way the rest of this package's
// test suite already does, rather than reimplementing the algorithm --
// a passing test here is evidence about the actual shipped code path, not
// about a parallel model of it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func ethSign(hash []byte, key *crypto.PrivateKey) ([]byte, error) {
	return ethcrypto.Sign(hash, key.PrivateKey)
}

func decodeSignedVote(t *testing.T, msg *p2p.Message) *SignedVote {
	t.Helper()
	if msg.Type != p2p.MsgTypeVote {
		t.Fatalf("expected a vote message, got type %d", msg.Type)
	}
	var sv SignedVote
	if err := json.Unmarshal(msg.Payload, &sv); err != nil {
		t.Fatalf("unmarshal vote: %v", err)
	}
	if sv.Vote == nil {
		t.Fatalf("decoded vote payload is nil")
	}
	return &sv
}

func decodeSignedProposal(t *testing.T, msg *p2p.Message) *SignedProposal {
	t.Helper()
	if msg.Type != p2p.MsgTypeProposal {
		t.Fatalf("expected a proposal message, got type %d", msg.Type)
	}
	var sp SignedProposal
	if err := json.Unmarshal(msg.Payload, &sp); err != nil {
		t.Fatalf("unmarshal proposal: %v", err)
	}
	if sp.Proposal == nil {
		t.Fatalf("decoded proposal payload is nil")
	}
	return &sp
}

// fourValidatorFixture builds a 4-validator, equal-weight set (3-of-4
// threshold) matching the audit's minimal reproduction, plus two candidate
// blocks at height 1 that a proposer might offer in different rounds.
type fourValidatorFixture struct {
	keys      []*crypto.PrivateKey
	addrs     [][]byte
	validator map[string]*big.Int
}

func newFourValidatorFixture(t *testing.T) *fourValidatorFixture {
	t.Helper()
	f := &fourValidatorFixture{validator: make(map[string]*big.Int, 4)}
	for i := 0; i < 4; i++ {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate validator key %d: %v", i, err)
		}
		addr := key.PubKey().Address().Bytes()
		f.keys = append(f.keys, key)
		f.addrs = append(f.addrs, addr)
		f.validator[string(addr)] = big.NewInt(1)
	}
	return f
}

func candidateBlock(height uint64, round int, proposer []byte, salt byte) *types.Block {
	header := &types.BlockHeader{
		Height:    height,
		Validator: proposer,
		// PrevHash is only used here to make two candidate blocks at the
		// same height hash differently -- a real node derives it from the
		// actual chain; tests only need distinct, stable identities.
		PrevHash: []byte{salt},
	}
	return types.NewBlock(header, nil)
}

// signedPrevote builds a real, correctly-signed prevote from validator idx
// for the given block hash, round, and height -- suitable for feeding into
// addVoteIfRelevant exactly as HandleVote would.
func (f *fourValidatorFixture) signedPrevote(t *testing.T, idx int, blockHash []byte, round int, height uint64) *SignedVote {
	t.Helper()
	vote := &Vote{BlockHash: blockHash, Round: round, Type: Prevote, Height: height}
	hash := sha256Sum(vote.bytes())
	sig, err := ethSign(hash, f.keys[idx])
	if err != nil {
		t.Fatalf("sign prevote for validator %d: %v", idx, err)
	}
	return &SignedVote{Vote: vote, Validator: f.addrs[idx], Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig}}
}

// TestStartNewRoundPreservesLockAcrossRoundTimeout is the single most
// direct regression test for the audit's finding: before this fix,
// startNewRound() unconditionally wiped a validator's just-established
// lock on every round timeout, with no memory that it had already
// precommitted a real block. This asserts the lock now survives an
// ordinary same-height round timeout.
func TestStartNewRoundPreservesLockAcrossRoundTimeout(t *testing.T) {
	f := newFourValidatorFixture(t)
	node := &trackingNode{validatorSet: f.validator}
	engine := NewEngine(node, f.keys[0], &recordingBroadcaster{})

	blockA := candidateBlock(1, 0, f.addrs[0], 0xAA)
	hashA, err := blockA.Header.Hash()
	if err != nil {
		t.Fatalf("hash block A: %v", err)
	}

	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 0}
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: blockA, Round: 0, ValidRound: -1}}
	engine.resetVoteTrackingLocked()
	engine.mu.Unlock()

	// Three of four validators prevote A -- reaches the 3-of-4 threshold,
	// so this validator's engine locks onto A at round 0.
	for i := 0; i < 3; i++ {
		added, reachedPrevote, _ := engine.addVoteIfRelevant(f.signedPrevote(t, i, hashA, 0, 1))
		if !added {
			t.Fatalf("expected prevote %d to be recorded", i)
		}
		_ = reachedPrevote
	}

	engine.mu.RLock()
	lockedRound := engine.lockedRound
	lockedHash := engine.lockedBlockHashLocked()
	engine.mu.RUnlock()
	if lockedRound != 0 || !bytes.Equal(lockedHash, hashA) {
		t.Fatalf("expected engine to lock onto block A at round 0 before timeout, got round=%d hash=%x", lockedRound, lockedHash)
	}

	// Round 0 times out with no commit (e.g. precommits never reached
	// quorum for this validator) -- startNewRound() is exactly what a real
	// commitTimer firing calls.
	engine.startNewRound()

	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.currentState.Round != 1 {
		t.Fatalf("expected round to advance to 1, got %d", engine.currentState.Round)
	}
	if engine.lockedRound != 0 {
		t.Fatalf("BUG REGRESSION: lock was cleared by a same-height round timeout (lockedRound=%d) -- this is exactly the audit's finding", engine.lockedRound)
	}
	if !bytes.Equal(engine.lockedBlockHashLocked(), hashA) {
		t.Fatalf("BUG REGRESSION: locked block changed across a round timeout, got %x want %x", engine.lockedBlockHashLocked(), hashA)
	}
}

// TestNoConflictingCommitAcrossRoundsUnderPartition is the master
// end-to-end regression test, reproducing the audit's scenario against the
// real production methods: a validator locks on block X in round 0 (a
// genuine 3-of-4 prevote Polka), round 0 then times out without this
// validator ever seeing precommit quorum (simulating the message-delay
// partition the audit describes), and a DIFFERENT block Y is freshly
// proposed in round 1. Before this fix, the validator would happily
// prevote for Y with no memory of X, letting Y pick up real quorum and
// fork against any earlier commit of X. After this fix, it must refuse.
func TestNoConflictingCommitAcrossRoundsUnderPartition(t *testing.T) {
	f := newFourValidatorFixture(t)
	node := &trackingNode{validatorSet: f.validator}
	broadcaster := &recordingBroadcaster{}
	engine := NewEngine(node, f.keys[0], broadcaster)

	blockX := candidateBlock(1, 0, f.addrs[1], 0x01)
	hashX, err := blockX.Header.Hash()
	if err != nil {
		t.Fatalf("hash block X: %v", err)
	}

	// --- Round 0: this validator receives proposal X and a real 3-of-4
	// prevote Polka for it (validators 0,1,2 -- itself included) ---
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 0}
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: blockX, Round: 0, ValidRound: -1}, Proposer: f.addrs[1]}
	engine.resetVoteTrackingLocked()
	engine.mu.Unlock()

	for i := 0; i < 3; i++ {
		if _, _, _ = engine.addVoteIfRelevant(f.signedPrevote(t, i, hashX, 0, 1)); false {
		}
	}

	engine.mu.RLock()
	if engine.lockedRound != 0 || !bytes.Equal(engine.lockedBlockHashLocked(), hashX) {
		engine.mu.RUnlock()
		t.Fatalf("setup failed: expected lock on X at round 0")
	}
	engine.mu.RUnlock()

	// Precommit quorum never arrives for this validator (the partition:
	// validator 3's precommit -- or enough others' -- never reaches it in
	// time). Round 0 times out.
	engine.startNewRound()

	engine.mu.RLock()
	if engine.currentState.Round != 1 {
		engine.mu.RUnlock()
		t.Fatalf("expected round 1 after timeout, got %d", engine.currentState.Round)
	}
	engine.mu.RUnlock()

	// --- Round 1: a different validator proposes a FRESH, different
	// block Y (ValidRound -1) -- exactly what the pre-fix propose() always
	// did, and exactly what an equivocating or simply round-robin-next
	// proposer would offer. ---
	blockY := candidateBlock(1, 1, f.addrs[2], 0x02)
	hashY, err := blockY.Header.Hash()
	if err != nil {
		t.Fatalf("hash block Y: %v", err)
	}
	if bytes.Equal(hashX, hashY) {
		t.Fatalf("test fixture bug: X and Y must hash differently")
	}

	engine.mu.Lock()
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: blockY, Round: 1, ValidRound: -1}, Proposer: f.addrs[2]}
	engine.mu.Unlock()

	broadcaster.messages = nil
	engine.prevote()

	if len(broadcaster.messages) != 1 {
		t.Fatalf("expected exactly one broadcast vote for round 1's prevote, got %d", len(broadcaster.messages))
	}
	vote := decodeSignedVote(t, broadcaster.messages[0])
	if vote.Vote.Type != Prevote {
		t.Fatalf("expected a prevote, got %v", vote.Vote.Type)
	}
	if len(vote.Vote.BlockHash) != 0 {
		t.Fatalf("SAFETY REGRESSION: validator prevoted for conflicting block Y (%x) despite being locked on X (%x) -- this is exactly the fork the audit demonstrated", vote.Vote.BlockHash, hashX)
	}

	// The nil prevote must never count toward Y's quorum.
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.activeProposal == nil {
		t.Fatalf("expected activeProposal to remain set (only the vote is nil, not the round state)")
	}
}

// TestPrevoteAllowsSwitchWhenPolkaIndependentlyObserved proves the
// liveness half is intact: a validator locked on X at round 0 MUST be
// willing to switch to a different block Y at a later round if it ITSELF
// independently observed a real Polka for Y at some round after its lock
// -- otherwise this fix would trade the audit's safety bug for a permanent
// stall.
func TestPrevoteAllowsSwitchWhenPolkaIndependentlyObserved(t *testing.T) {
	f := newFourValidatorFixture(t)
	node := &trackingNode{validatorSet: f.validator}
	broadcaster := &recordingBroadcaster{}
	engine := NewEngine(node, f.keys[0], broadcaster)

	blockX := candidateBlock(1, 0, f.addrs[1], 0x10)
	hashX, _ := blockX.Header.Hash()
	blockY := candidateBlock(1, 1, f.addrs[2], 0x20)
	hashY, _ := blockY.Header.Hash()

	// Lock on X at round 0.
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 0}
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: blockX, Round: 0, ValidRound: -1}}
	engine.resetVoteTrackingLocked()
	engine.mu.Unlock()
	for i := 0; i < 3; i++ {
		engine.addVoteIfRelevant(f.signedPrevote(t, i, hashX, 0, 1))
	}
	engine.startNewRound() // -> round 1

	// Round 1: this validator independently observes a real 3-of-4 Polka
	// for Y (e.g. it received the round-1 proposal and enough peers'
	// prevotes for it), advancing its own lock to Y at round 1.
	engine.mu.Lock()
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: blockY, Round: 1, ValidRound: -1}}
	engine.resetVoteTrackingLocked()
	engine.mu.Unlock()
	for i := 0; i < 3; i++ {
		engine.addVoteIfRelevant(f.signedPrevote(t, i, hashY, 1, 1))
	}
	engine.mu.RLock()
	if engine.lockedRound != 1 || !bytes.Equal(engine.lockedBlockHashLocked(), hashY) {
		engine.mu.RUnlock()
		t.Fatalf("setup failed: expected lock to move to Y at round 1")
	}
	engine.mu.RUnlock()
	engine.startNewRound() // -> round 2, lock on Y/round1 must survive (same height)

	// Round 2: proposer re-proposes Y, honestly citing ValidRound=1 -- the
	// real Polka this validator itself just observed.
	engine.mu.Lock()
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: blockY, Round: 2, ValidRound: 1}}
	engine.mu.Unlock()

	broadcaster.messages = nil
	engine.prevote()

	if len(broadcaster.messages) != 1 {
		t.Fatalf("expected one broadcast vote, got %d", len(broadcaster.messages))
	}
	vote := decodeSignedVote(t, broadcaster.messages[0])
	if !bytes.Equal(vote.Vote.BlockHash, hashY) {
		t.Fatalf("LIVENESS REGRESSION: validator refused to re-affirm Y despite a genuinely observed Polka backing it (got blockHash=%x want %x)", vote.Vote.BlockHash, hashY)
	}
}

// TestPrevoteRejectsUnverifiedValidRoundClaim proves the core defense
// against a lying or equivocating proposer: a proposal claiming
// ValidRound=vr for some block is only honored if THIS validator itself
// recorded a matching Polka at round vr. A bare claim, with no matching
// local observation, must never be enough to move a lock.
func TestPrevoteRejectsUnverifiedValidRoundClaim(t *testing.T) {
	f := newFourValidatorFixture(t)
	node := &trackingNode{validatorSet: f.validator}
	broadcaster := &recordingBroadcaster{}
	engine := NewEngine(node, f.keys[0], broadcaster)

	blockX := candidateBlock(1, 0, f.addrs[1], 0x30)
	hashX, _ := blockX.Header.Hash()
	blockZ := candidateBlock(1, 1, f.addrs[2], 0x40) // this validator never saw a Polka for Z at any round

	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 0}
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: blockX, Round: 0, ValidRound: -1}}
	engine.resetVoteTrackingLocked()
	engine.mu.Unlock()
	for i := 0; i < 3; i++ {
		engine.addVoteIfRelevant(f.signedPrevote(t, i, hashX, 0, 1))
	}
	engine.startNewRound() // -> round 1, still locked on X/round0, no round-1 activity at all

	// Round 2: a proposer falsely (or just incorrectly) claims Z had a
	// Polka at round 1 -- but this validator's own polkaHistory has
	// nothing recorded for round 1 at all.
	engine.mu.Lock()
	engine.currentState.Round = 2
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: blockZ, Round: 2, ValidRound: 1}}
	engine.prevoteSent = false
	engine.mu.Unlock()

	broadcaster.messages = nil
	engine.prevote()

	if len(broadcaster.messages) != 1 {
		t.Fatalf("expected one broadcast vote, got %d", len(broadcaster.messages))
	}
	vote := decodeSignedVote(t, broadcaster.messages[0])
	if len(vote.Vote.BlockHash) != 0 {
		t.Fatalf("SAFETY REGRESSION: validator accepted an unverified ValidRound claim and prevoted for Z (%x) with no locally-observed Polka to back it", vote.Vote.BlockHash)
	}
}

// TestProposeReProposesValidBlockInsteadOfFreshMempoolBlock proves the
// liveness-side companion change in propose(): once this validator has
// observed a Polka (e.currentState's validBlock), becoming proposer again
// must re-propose that exact value rather than manufacturing a brand new
// block from mempool, citing the correct ValidRound.
func TestProposeReProposesValidBlockInsteadOfFreshMempoolBlock(t *testing.T) {
	f := newFourValidatorFixture(t)
	node := &emptyBlockNode{validatorSet: f.validator, validator: f.addrs[0]}
	broadcaster := &recordingBroadcaster{}
	engine := NewEngine(node, f.keys[0], broadcaster)

	validBlock := candidateBlock(1, 1, f.addrs[1], 0x55)

	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 3}
	engine.validBlock = validBlock
	engine.validRound = 1
	engine.mu.Unlock()

	if err := engine.propose(); err != nil {
		t.Fatalf("propose: %v", err)
	}

	if len(broadcaster.messages) != 1 {
		t.Fatalf("expected one broadcast proposal, got %d", len(broadcaster.messages))
	}
	sp := decodeSignedProposal(t, broadcaster.messages[0])
	if sp.Proposal.ValidRound != 1 {
		t.Fatalf("expected re-proposal to carry ValidRound=1, got %d", sp.Proposal.ValidRound)
	}
	gotHash, _ := sp.Proposal.Block.Header.Hash()
	wantHash, _ := validBlock.Header.Hash()
	if !bytes.Equal(gotHash, wantHash) {
		t.Fatalf("expected re-proposal to reuse the exact valid block (hash %x), got a different block (hash %x) -- proposer must not fabricate a fresh value once a Polka exists", wantHash, gotHash)
	}
	if len(node.committed) != 0 {
		t.Fatalf("propose() must not itself commit anything")
	}
}

// TestVerifySignedProposalAllowsReProposalByDifferentAuthor proves the
// wire-format relaxation needed for re-proposal is itself narrowly scoped:
// a re-proposal (ValidRound>=0) may legitimately carry a different
// Proposer than the block's original Header.Validator, but an ordinary
// fresh proposal (ValidRound<0) must still be rejected if they mismatch.
func TestVerifySignedProposalAllowsReProposalByDifferentAuthor(t *testing.T) {
	f := newFourValidatorFixture(t)
	node := &trackingNode{validatorSet: f.validator}
	engine := NewEngine(node, f.keys[0], &recordingBroadcaster{})

	// blockA was originally authored (Header.Validator) by validator 1,
	// but validator 2 is re-proposing it this round, honestly citing
	// ValidRound=0.
	blockA := candidateBlock(1, 0, f.addrs[1], 0x60)
	reproposal := &Proposal{Block: blockA, Round: 2, ValidRound: 0}
	hash := sha256Sum(reproposal.bytes())
	sig, err := ethSign(hash, f.keys[2])
	if err != nil {
		t.Fatalf("sign re-proposal: %v", err)
	}
	signed := &SignedProposal{Proposal: reproposal, Proposer: f.addrs[2], Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig}}

	if err := engine.verifySignedProposal(signed); err != nil {
		t.Fatalf("expected a legitimate re-proposal by a different validator to verify, got error: %v", err)
	}

	// Same block and signer, but claiming to be a FRESH proposal
	// (ValidRound -1) instead of an honest re-proposal -- Header.Validator
	// (validator 1) and Proposer (validator 2) mismatch, and a fresh claim
	// gets no benefit of the doubt.
	fresh := &Proposal{Block: blockA, Round: 2, ValidRound: -1}
	freshHash := sha256Sum(fresh.bytes())
	freshSig, err := ethSign(freshHash, f.keys[2])
	if err != nil {
		t.Fatalf("sign fresh proposal: %v", err)
	}
	freshSigned := &SignedProposal{Proposal: fresh, Proposer: f.addrs[2], Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: freshSig}}
	if err := engine.verifySignedProposal(freshSigned); err == nil {
		t.Fatalf("expected a fresh proposal with mismatched Proposer/Header.Validator to be rejected")
	}
}

// TestResetLockStateOnlyHappensOnHeightAdvance proves startNewRound()
// distinguishes an ordinary same-height round bump (lock survives) from a
// genuine height advance via the node-height-resync path (lock must
// clear -- an old height's lock must never leak into the next height's
// decisions).
func TestResetLockStateOnlyHappensOnHeightAdvance(t *testing.T) {
	f := newFourValidatorFixture(t)
	node := &trackingNode{validatorSet: f.validator, height: 5}
	engine := NewEngine(node, f.keys[0], &recordingBroadcaster{})

	engine.mu.Lock()
	engine.currentState = State{Height: 2, Round: 3}
	engine.lockedBlock = candidateBlock(2, 1, f.addrs[1], 0x70)
	engine.lockedRound = 1
	engine.validBlock = engine.lockedBlock
	engine.validRound = 1
	engine.polkaHistory[1] = polkaRecord{block: engine.lockedBlock, blockHash: []byte{1, 2, 3}}
	engine.mu.Unlock()

	engine.startNewRound() // node is at height 5; engine resyncs to height 6

	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.currentState.Height != 6 {
		t.Fatalf("expected engine to resync to height 6, got %d", engine.currentState.Height)
	}
	if engine.lockedBlock != nil || engine.lockedRound != -1 || engine.validBlock != nil || engine.validRound != -1 {
		t.Fatalf("expected lock/valid state to be cleared on height advance, got lockedRound=%d validRound=%d", engine.lockedRound, engine.validRound)
	}
	if len(engine.polkaHistory) != 0 {
		t.Fatalf("expected polkaHistory to be cleared on height advance, got %d entries", len(engine.polkaHistory))
	}
}
