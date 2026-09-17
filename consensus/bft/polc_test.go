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
// A first version of this fix verified a re-proposal's ValidRound claim by
// checking THIS validator's own historical vote tally. That version was
// deployed to production, immediately caused a real liveness incident (two
// validators permanently deadlocked at one height), and was rolled back.
// Root cause: one validator's round counter can legitimately race ahead of
// another's before a same-round vote physically arrives -- this codebase's
// HandleVote/HandleProposal both drop any message whose round is older
// than the receiver's current round -- so the validator that raced ahead
// can never retroactively confirm a polka a peer already locked on, and
// neither can ever satisfy the other's lock again.
//
// This version instead makes Proof-of-Lock-Change verification purely
// cryptographic (Engine.verifyPolkaProofLocked): a re-proposal carries the
// actual signed prevotes constituting the claimed Polka, and any receiver
// verifies them directly against the validator set, with no dependency on
// having personally witnessed the original round's gossip in real time.
// TestReProposalResolvesRealDeadlockScenario below is the direct
// regression test for the actual production incident.
//
// All tests drive the real production methods (addVoteIfRelevant, prevote,
// precommit, propose, startNewRound, verifySignedProposal,
// lockCompliesLocked, verifyPolkaProofLocked) directly, the same way the
// rest of this package's test suite already does, rather than
// reimplementing the algorithm -- a passing test here is evidence about the
// actual shipped code path, not about a parallel model of it.

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
// threshold) matching the audit's minimal reproduction, plus helpers for
// building candidate blocks and real signed prevotes.
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

// twoValidatorFixture builds a 2-validator, equal-weight set (unanimous,
// 2-of-2 threshold) matching the ACTUAL production topology that hit the
// real deadlock incident this test file's centerpiece test reproduces.
type twoValidatorFixture struct {
	keys      []*crypto.PrivateKey
	addrs     [][]byte
	validator map[string]*big.Int
}

func newTwoValidatorFixture(t *testing.T) *twoValidatorFixture {
	t.Helper()
	f := &twoValidatorFixture{validator: make(map[string]*big.Int, 2)}
	for i := 0; i < 2; i++ {
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

func signPrevote(t *testing.T, key *crypto.PrivateKey, addr []byte, blockHash []byte, round int, height uint64) *SignedVote {
	t.Helper()
	vote := &Vote{BlockHash: blockHash, Round: round, Type: Prevote, Height: height}
	hash := sha256Sum(vote.bytes())
	sig, err := ethSign(hash, key)
	if err != nil {
		t.Fatalf("sign prevote: %v", err)
	}
	return &SignedVote{Vote: vote, Validator: addr, Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig}}
}

func (f *fourValidatorFixture) signedPrevote(t *testing.T, idx int, blockHash []byte, round int, height uint64) *SignedVote {
	t.Helper()
	return signPrevote(t, f.keys[idx], f.addrs[idx], blockHash, round, height)
}

func (f *twoValidatorFixture) signedPrevote(t *testing.T, idx int, blockHash []byte, round int, height uint64) *SignedVote {
	t.Helper()
	return signPrevote(t, f.keys[idx], f.addrs[idx], blockHash, round, height)
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
		added, _, _ := engine.addVoteIfRelevant(f.signedPrevote(t, i, hashA, 0, 1))
		if !added {
			t.Fatalf("expected prevote %d to be recorded", i)
		}
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

// TestNoConflictingCommitAcrossRoundsUnderPartition reproduces the
// audit's scenario against the real production methods: a validator locks
// on block X in round 0 (a genuine 3-of-4 prevote Polka), round 0 then
// times out without this validator ever seeing precommit quorum
// (simulating the message-delay partition the audit describes), and a
// DIFFERENT block Y is freshly proposed in round 1. Before this fix, the
// validator would happily prevote for Y with no memory of X, letting Y
// pick up real quorum and fork against any earlier commit of X. After
// this fix, it must refuse.
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

	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 0}
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: blockX, Round: 0, ValidRound: -1}, Proposer: f.addrs[1]}
	engine.resetVoteTrackingLocked()
	engine.mu.Unlock()

	for i := 0; i < 3; i++ {
		engine.addVoteIfRelevant(f.signedPrevote(t, i, hashX, 0, 1))
	}

	engine.mu.RLock()
	if engine.lockedRound != 0 || !bytes.Equal(engine.lockedBlockHashLocked(), hashX) {
		engine.mu.RUnlock()
		t.Fatalf("setup failed: expected lock on X at round 0")
	}
	engine.mu.RUnlock()

	engine.startNewRound()

	engine.mu.RLock()
	if engine.currentState.Round != 1 {
		engine.mu.RUnlock()
		t.Fatalf("expected round 1 after timeout, got %d", engine.currentState.Round)
	}
	engine.mu.RUnlock()

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
}

// TestReProposalResolvesRealDeadlockScenario is the direct regression test
// for the ACTUAL PRODUCTION INCIDENT (not the audit's hypothetical): a
// 2-validator network (exactly this chain's real topology) where validator
// B independently reaches a real prevote Polka for block Y at round 1 and
// locks onto it, while validator A's own round counter races ahead to a
// later round BEFORE A ever processes that same vote -- so A's engine
// never independently records round 1's Polka at all (this is simulated
// by simply never delivering that vote to A, exactly matching
// HandleVote's real behavior of dropping a vote whose round is older than
// the receiver's current round). Under the FIRST (rolled-back) version of
// this fix, A and B could never resolve this and deadlocked permanently.
// This test proves the cryptographic-proof design resolves it: once B
// becomes proposer again and re-proposes Y with the actual signed
// prevotes attached, A accepts it purely by verifying those signatures --
// with no dependency on A having witnessed round 1's gossip itself.
func TestReProposalResolvesRealDeadlockScenario(t *testing.T) {
	f := newTwoValidatorFixture(t)

	// --- Validator B's engine: independently locks on Y at round 1 ---
	nodeB := &trackingNode{validatorSet: f.validator}
	engineB := NewEngine(nodeB, f.keys[1], &recordingBroadcaster{})

	blockY := candidateBlock(1, 1, f.addrs[1], 0x99)
	hashY, err := blockY.Header.Hash()
	if err != nil {
		t.Fatalf("hash block Y: %v", err)
	}

	engineB.mu.Lock()
	engineB.currentState = State{Height: 1, Round: 1}
	engineB.activeProposal = &SignedProposal{Proposal: &Proposal{Block: blockY, Round: 1, ValidRound: -1}, Proposer: f.addrs[1]}
	engineB.resetVoteTrackingLocked()
	engineB.mu.Unlock()

	// Both validators' prevotes for Y at round 1 -- with n=2, threshold=2,
	// this genuinely requires both, matching the real incident's topology.
	engineB.addVoteIfRelevant(f.signedPrevote(t, 0, hashY, 1, 1))
	engineB.addVoteIfRelevant(f.signedPrevote(t, 1, hashY, 1, 1))

	engineB.mu.RLock()
	if engineB.lockedRound != 1 || !bytes.Equal(engineB.lockedBlockHashLocked(), hashY) {
		engineB.mu.RUnlock()
		t.Fatalf("setup failed: expected validator B to lock on Y at round 1")
	}
	proof := engineB.polkaHistory[1].votes
	engineB.mu.RUnlock()
	if len(proof) != 2 {
		t.Fatalf("expected validator B to have snapshotted 2 signed prevotes as proof, got %d", len(proof))
	}

	// --- Validator A's engine: NEVER saw round 1's votes (its round
	// counter raced ahead before they arrived -- exactly the real
	// incident's root cause) -- it is simply unlocked, at a later round,
	// with an empty polka history. ---
	nodeA := &trackingNode{validatorSet: f.validator}
	broadcasterA := &recordingBroadcaster{}
	engineA := NewEngine(nodeA, f.keys[0], broadcasterA)
	engineA.mu.Lock()
	engineA.currentState = State{Height: 1, Round: 6}
	engineA.mu.Unlock()

	if len(engineA.polkaHistory) != 0 {
		t.Fatalf("test setup bug: engine A must start with no polka history, to genuinely simulate never having witnessed round 1")
	}

	// Validator B becomes proposer again at round 6 and honestly
	// re-proposes Y, attaching the real signed prevotes as proof.
	engineA.mu.Lock()
	engineA.activeProposal = &SignedProposal{
		Proposal: &Proposal{Block: blockY, Round: 6, ValidRound: 1, ValidRoundProof: proof},
		Proposer: f.addrs[1],
	}
	engineA.mu.Unlock()

	broadcasterA.messages = nil
	engineA.prevote()

	if len(broadcasterA.messages) != 1 {
		t.Fatalf("expected one broadcast vote from A, got %d", len(broadcasterA.messages))
	}
	vote := decodeSignedVote(t, broadcasterA.messages[0])
	if !bytes.Equal(vote.Vote.BlockHash, hashY) {
		t.Fatalf("DEADLOCK REGRESSION: validator A refused to accept validator B's honestly re-proposed, cryptographically proven value Y (got blockHash=%x, want %x) -- this is exactly the production incident this fix must resolve", vote.Vote.BlockHash, hashY)
	}
}

// TestVerifyPolkaProofLockedRejectsForgedOrInsufficientProof proves the
// verification is real cryptographic work, not a rubber stamp: a claim
// backed by too few valid signatures, or signatures for the wrong
// round/block/height, must be rejected.
func TestVerifyPolkaProofLockedRejectsForgedOrInsufficientProof(t *testing.T) {
	f := newFourValidatorFixture(t)
	node := &trackingNode{validatorSet: f.validator}
	engine := NewEngine(node, f.keys[0], &recordingBroadcaster{})
	engine.mu.Lock()
	engine.totalVotingPower = big.NewInt(4)
	engine.validatorSet = f.validator
	engine.mu.Unlock()

	block := candidateBlock(1, 5, f.addrs[0], 0x77)
	hash, _ := block.Header.Hash()

	// Only 2 of 4 validators signed -- below the 3-of-4 threshold.
	insufficient := []*SignedVote{
		f.signedPrevote(t, 0, hash, 2, 1),
		f.signedPrevote(t, 1, hash, 2, 1),
	}
	engine.mu.RLock()
	ok := engine.verifyPolkaProofLocked(insufficient, 2, hash, 1)
	engine.mu.RUnlock()
	if ok {
		t.Fatalf("expected insufficient signature set (2 of 4) to fail verification")
	}

	// 3 of 4 signed, but for a DIFFERENT round than claimed.
	wrongRound := []*SignedVote{
		f.signedPrevote(t, 0, hash, 3, 1), // round 3, not round 2
		f.signedPrevote(t, 1, hash, 3, 1),
		f.signedPrevote(t, 2, hash, 3, 1),
	}
	engine.mu.RLock()
	ok = engine.verifyPolkaProofLocked(wrongRound, 2, hash, 1)
	engine.mu.RUnlock()
	if ok {
		t.Fatalf("expected signatures for the wrong round to fail verification against the claimed round")
	}

	// 3 of 4 signed for the right round/height, but a different block hash.
	otherBlock := candidateBlock(1, 5, f.addrs[0], 0x88)
	otherHash, _ := otherBlock.Header.Hash()
	wrongBlock := []*SignedVote{
		f.signedPrevote(t, 0, otherHash, 2, 1),
		f.signedPrevote(t, 1, otherHash, 2, 1),
		f.signedPrevote(t, 2, otherHash, 2, 1),
	}
	engine.mu.RLock()
	ok = engine.verifyPolkaProofLocked(wrongBlock, 2, hash, 1)
	engine.mu.RUnlock()
	if ok {
		t.Fatalf("expected signatures for a different block hash to fail verification")
	}

	// A genuinely sufficient, correctly-targeted proof must succeed.
	valid := []*SignedVote{
		f.signedPrevote(t, 0, hash, 2, 1),
		f.signedPrevote(t, 1, hash, 2, 1),
		f.signedPrevote(t, 2, hash, 2, 1),
	}
	engine.mu.RLock()
	ok = engine.verifyPolkaProofLocked(valid, 2, hash, 1)
	engine.mu.RUnlock()
	if !ok {
		t.Fatalf("expected a genuine 3-of-4 proof to verify successfully")
	}
}

// TestPrevoteRejectsForgedValidRoundClaim proves prevote()'s end-to-end
// gate rejects a proposal claiming a Polka it can't actually prove --
// e.g. a proposer that fabricates ValidRound without real signatures, or
// reuses signatures that don't cover the claim.
func TestPrevoteRejectsForgedValidRoundClaim(t *testing.T) {
	f := newFourValidatorFixture(t)
	node := &trackingNode{validatorSet: f.validator}
	broadcaster := &recordingBroadcaster{}
	engine := NewEngine(node, f.keys[0], broadcaster)

	blockZ := candidateBlock(1, 1, f.addrs[2], 0x40)

	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 2}
	engine.activeProposal = &SignedProposal{
		Proposal: &Proposal{Block: blockZ, Round: 2, ValidRound: 1, ValidRoundProof: nil}, // no proof at all
		Proposer: f.addrs[2],
	}
	engine.mu.Unlock()

	broadcaster.messages = nil
	engine.prevote()

	if len(broadcaster.messages) != 1 {
		t.Fatalf("expected one broadcast vote, got %d", len(broadcaster.messages))
	}
	vote := decodeSignedVote(t, broadcaster.messages[0])
	if len(vote.Vote.BlockHash) != 0 {
		t.Fatalf("SAFETY REGRESSION: validator accepted a ValidRound claim with no supporting proof at all")
	}
}

// TestProposeReProposesValidBlockInsteadOfFreshMempoolBlock proves the
// liveness-side companion change in propose(): once this validator has
// observed a Polka (e.currentState's validBlock), becoming proposer again
// must re-propose that exact value rather than manufacturing a brand new
// block from mempool, citing the correct ValidRound and attaching the
// real proof.
func TestProposeReProposesValidBlockInsteadOfFreshMempoolBlock(t *testing.T) {
	f := newFourValidatorFixture(t)
	node := &emptyBlockNode{validatorSet: f.validator, validator: f.addrs[0]}
	broadcaster := &recordingBroadcaster{}
	engine := NewEngine(node, f.keys[0], broadcaster)

	validBlock := candidateBlock(1, 1, f.addrs[1], 0x55)
	validHash, _ := validBlock.Header.Hash()
	proof := []*SignedVote{
		f.signedPrevote(t, 0, validHash, 1, 1),
		f.signedPrevote(t, 1, validHash, 1, 1),
		f.signedPrevote(t, 2, validHash, 1, 1),
	}

	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 3}
	engine.validBlock = validBlock
	engine.validRound = 1
	engine.polkaHistory[1] = polkaRecord{block: validBlock, blockHash: validHash, votes: proof}
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
	if len(sp.Proposal.ValidRoundProof) != 3 {
		t.Fatalf("expected re-proposal to attach the 3-signature proof, got %d", len(sp.Proposal.ValidRoundProof))
	}
	gotHash, _ := sp.Proposal.Block.Header.Hash()
	if !bytes.Equal(gotHash, validHash) {
		t.Fatalf("expected re-proposal to reuse the exact valid block (hash %x), got a different block (hash %x) -- proposer must not fabricate a fresh value once a Polka exists", validHash, gotHash)
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

	lockedBlock := candidateBlock(2, 1, f.addrs[1], 0x70)
	lockedHash, _ := lockedBlock.Header.Hash()

	engine.mu.Lock()
	engine.currentState = State{Height: 2, Round: 3}
	engine.lockedBlock = lockedBlock
	engine.lockedRound = 1
	engine.validBlock = engine.lockedBlock
	engine.validRound = 1
	engine.polkaHistory[1] = polkaRecord{block: engine.lockedBlock, blockHash: lockedHash, votes: nil}
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
