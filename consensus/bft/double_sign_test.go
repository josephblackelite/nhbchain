package bft

// Double-sign regression tests.
//
// An honest validator must never sign two votes for the same height, round and
// vote type that name different blocks: any two such votes are a complete
// slashing proof (consensus/potso/evidence.VerifyEquivocationProof), and the
// release slashes the offender's whole stake on one. Before the guard in
// createVote (see sign_state.go) three ordinary paths did it anyway, and every
// test below fails on the engine without the guard:
//
//   - a second proposal for the round, bad after good or good after bad, drew a
//     second, different prevote (one byzantine validator was enough);
//   - a block that failed to commit drew a prevote for nil after this
//     validator's own prevote and precommit for it, and the reset that follows let
//     a new proposal in the same round be voted again;
//   - a restart forgot every vote, so a proposer that restarted between voting and
//     committing built a different block for the same round and signed that too.
//
// Each test asserts on what the validator actually put on the wire, and on
// whether the real evidence verifier can build a valid proof from it -- not on
// the guard's own bookkeeping -- so it holds however the guard is written.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nhbchain/consensus/potso/evidence"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"
)

// badSalt is the first byte of the PrevHash of a block the test node refuses to
// validate (see badBlock); goodBlock's salts never start with it.
const badSalt byte = 0xBD

// goodBlock is a valid candidate block for the round; salt tells blocks apart.
func goodBlock(round int, proposer []byte, salt byte) *types.Block {
	return candidateBlock(1, round, proposer, salt)
}

// badBlock is a candidate block that fails validation; tag tells bad blocks apart.
func badBlock(round int, proposer []byte, tag byte) *types.Block {
	header := &types.BlockHeader{Height: 1, Validator: proposer, PrevHash: []byte{badSalt, tag}}
	return types.NewBlock(header, nil)
}

// signGuardNode is a NodeInterface double for these tests. Unlike livenessNode
// it can be used by a running round loop and by the test at once, it builds a
// different block on every call (a real proposer stamps a new time and offers
// a new mempool each time), and it lets a test choose which blocks fail
// validation and whether a commit fails.
type signGuardNode struct {
	mu           sync.Mutex
	validatorSet map[string]*big.Int
	self         []byte
	height       uint64
	seq          int
	commitErr    error
	validated    [][]byte // header hashes ValidateBlock was asked about
	committed    []*types.Block
}

func (n *signGuardNode) GetMempool() []*types.Transaction         { return nil }
func (n *signGuardNode) RequeueTransactions([]*types.Transaction) {}
func (n *signGuardNode) CreateBlock([]*types.Transaction) (*types.Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seq++
	header := &types.BlockHeader{Height: n.height + 1, Validator: n.self, PrevHash: []byte(fmt.Sprintf("built-%d", n.seq))}
	return types.NewBlock(header, nil), nil
}
func (n *signGuardNode) ValidateBlock(b *types.Block) error {
	if b == nil || b.Header == nil {
		return errors.New("nil block")
	}
	hash, _ := b.Header.Hash()
	n.mu.Lock()
	n.validated = append(n.validated, hash)
	n.mu.Unlock()
	if len(b.Header.PrevHash) > 0 && b.Header.PrevHash[0] == badSalt {
		return errors.New("bad block")
	}
	return nil
}
func (n *signGuardNode) CommitBlock(b *types.Block) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.commitErr != nil {
		return n.commitErr
	}
	n.committed = append(n.committed, b)
	n.height = b.Header.Height
	return nil
}
func (n *signGuardNode) GetValidatorSet() map[string]*big.Int { return n.validatorSet }
func (n *signGuardNode) GetAccount(addr []byte) (*types.Account, error) {
	weight := n.validatorSet[string(addr)]
	if weight == nil {
		weight = big.NewInt(0)
	}
	return &types.Account{Stake: new(big.Int).Set(weight)}, nil
}
func (n *signGuardNode) GetLastCommitHash() []byte { return nil }
func (n *signGuardNode) GetHeight() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.height
}

// applySynced is what a node does with a block a peer has committed and sent: if
// it is the next height's block, it is committed (production also checks the
// quorum certificate; nothing here depends on that).
func (n *signGuardNode) applySynced(b *types.Block) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if b == nil || b.Header == nil || b.Header.Height != n.height+1 {
		return
	}
	n.committed = append(n.committed, b)
	n.height = b.Header.Height
}

func (n *signGuardNode) validatedHashes() [][]byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([][]byte(nil), n.validated...)
}
func (n *signGuardNode) committedHash(t *testing.T) []byte {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.committed) == 0 {
		return nil
	}
	hash, err := n.committed[0].Header.Hash()
	if err != nil {
		t.Fatalf("hash committed block: %v", err)
	}
	return hash
}

// captureBroadcaster records every message the engine broadcasts.
type captureBroadcaster struct {
	mu     sync.Mutex
	msgs   []*p2p.Message
	onVote func(*SignedVote) // called for each vote message before it is recorded
}

func (c *captureBroadcaster) Broadcast(msg *p2p.Message) error {
	if msg.Type == p2p.MsgTypeVote && c.onVote != nil {
		var sv SignedVote
		if err := json.Unmarshal(msg.Payload, &sv); err == nil {
			c.onVote(&sv)
		}
	}
	c.mu.Lock()
	c.msgs = append(c.msgs, msg)
	c.mu.Unlock()
	return nil
}

func (c *captureBroadcaster) messages() []*p2p.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*p2p.Message(nil), c.msgs...)
}

// votesBy decodes every vote in msgs that signer signed.
func votesBy(msgs []*p2p.Message, signer []byte) []*SignedVote {
	var out []*SignedVote
	for _, m := range msgs {
		if m.Type != p2p.MsgTypeVote {
			continue
		}
		var sv SignedVote
		if err := json.Unmarshal(m.Payload, &sv); err != nil || sv.Vote == nil || sv.Signature == nil {
			continue
		}
		if bytes.Equal(sv.Validator, signer) {
			out = append(out, &sv)
		}
	}
	return out
}

func (c *captureBroadcaster) votes(signer []byte) []*SignedVote {
	return votesBy(c.messages(), signer)
}

// equivocationProofOf is the slashing report a bystander would file for two
// votes: the votes' own signatures and hashes, nothing else.
func equivocationProofOf(a, b *SignedVote) []byte {
	proof := evidence.EquivocationProof{
		Height:   a.Vote.Height,
		Round:    a.Vote.Round,
		VoteType: evidence.EquivocationVoteType(a.Vote.Type),
		VoteA:    evidence.EquivocationSignedVote{BlockHash: hex.EncodeToString(a.Vote.BlockHash), Signature: hex.EncodeToString(a.Signature.Signature)},
		VoteB:    evidence.EquivocationSignedVote{BlockHash: hex.EncodeToString(b.Vote.BlockHash), Signature: hex.EncodeToString(b.Signature.Signature)},
	}
	raw, _ := json.Marshal(proof)
	return raw
}

// slashableProofs counts the pairs among votes that the real evidence verifier
// accepts as a proof of double-signing by offender.
func slashableProofs(votes []*SignedVote, offender []byte) int {
	var off [20]byte
	copy(off[:], offender)
	count := 0
	for i, a := range votes {
		for _, b := range votes[i+1:] {
			if a.Vote.Height != b.Vote.Height || a.Vote.Round != b.Vote.Round || a.Vote.Type != b.Vote.Type {
				continue
			}
			if evidence.VerifyEquivocationProof(off, equivocationProofOf(a, b)) == nil {
				count++
			}
		}
	}
	return count
}

func requireNoSlashableProof(t *testing.T, votes []*SignedVote, offender []byte) {
	t.Helper()
	if n := slashableProofs(votes, offender); n != 0 {
		var lines []string
		for _, v := range votes {
			lines = append(lines, fmt.Sprintf("  %s height=%d round=%d hash=%x", v.Vote.Type, v.Vote.Height, v.Vote.Round, v.Vote.BlockHash))
		}
		t.Fatalf("HONEST VALIDATOR DOUBLE-SIGNED: %d valid slashing proof(s) can be built from the votes it broadcast:\n%s", n, joinLines(lines))
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

// TestSlashableProofCheckerFindsARealDoubleSign proves the checker the other
// tests rely on is not vacuous: two conflicting votes signed by one key ARE
// reported, and two different validators' votes, or the same vote twice, are not.
func TestSlashableProofCheckerFindsARealDoubleSign(t *testing.T) {
	tv := newTestValidators(t, 2)
	hashA, hashB := sha256Sum([]byte("a")), sha256Sum([]byte("b"))

	conflicting := []*SignedVote{
		signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hashA, 3, 1),
		signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hashB, 3, 1),
	}
	if n := slashableProofs(conflicting, tv.addrs[0]); n != 1 {
		t.Fatalf("expected the checker to find the double-sign, found %d proofs", n)
	}
	nilAgainstBlock := []*SignedVote{
		signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hashA, 3, 1),
		signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, nil, 3, 1),
	}
	if n := slashableProofs(nilAgainstBlock, tv.addrs[0]); n != 1 {
		t.Fatalf("a nil prevote against a block prevote is a double-sign; the checker found %d proofs", n)
	}
	harmless := []*SignedVote{
		signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hashA, 3, 1),
		signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hashA, 3, 1),   // the same vote again
		signVoteAs(t, tv.keys[0], tv.addrs[0], Precommit, hashB, 3, 1), // another type
		signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hashB, 4, 1),   // another round
		signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hashB, 3, 2),   // another height
		signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hashB, 3, 1),   // another validator
		signVoteAs(t, tv.keys[0], tv.addrs[0], Precommit, hashA, 4, 1), // another round and type
	}
	if n := slashableProofs(harmless, tv.addrs[0]); n != 0 {
		t.Fatalf("no double-sign among these votes, the checker found %d proofs", n)
	}
}

// testValidators is a set of equal-weight validators with their keys.
type testValidators struct {
	keys  []*crypto.PrivateKey
	addrs [][]byte
	set   map[string]*big.Int
}

func newTestValidators(t *testing.T, n int) *testValidators {
	t.Helper()
	tv := &testValidators{set: make(map[string]*big.Int, n)}
	for i := 0; i < n; i++ {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate validator key %d: %v", i, err)
		}
		addr := key.PubKey().Address().Bytes()
		tv.keys = append(tv.keys, key)
		tv.addrs = append(tv.addrs, addr)
		tv.set[string(addr)] = big.NewInt(1)
	}
	return tv
}

// validatorsWithProposer returns n validators in which the one at index want is
// the proposer of round. The proposer of a round depends on the order of the
// addresses, so keys are drawn until the draw comes out right.
func validatorsWithProposer(t *testing.T, n, round, want int) *testValidators {
	t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		tv := newTestValidators(t, n)
		probe := NewEngine(&signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}, tv.keys[0], &captureBroadcaster{})
		if bytes.Equal(probe.selectProposer(round), tv.addrs[want]) {
			return tv
		}
	}
	t.Fatalf("no draw of %d validators made index %d the proposer of round %d", n, want, round)
	return nil
}

// newVoter builds the engine of validator 0 of tv (the one under test).
func newVoter(t *testing.T, tv *testValidators, opts ...Option) (*Engine, *signGuardNode, *captureBroadcaster) {
	t.Helper()
	node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}
	br := &captureBroadcaster{}
	return NewEngine(node, tv.keys[0], br, opts...), node, br
}

func newSignedProposal(t *testing.T, key *crypto.PrivateKey, addr []byte, block *types.Block, round int) *SignedProposal {
	t.Helper()
	proposal := &Proposal{Block: block, Round: round, ValidRound: -1}
	sig, err := ethSign(sha256Sum(proposal.bytes()), key)
	if err != nil {
		t.Fatalf("sign proposal: %v", err)
	}
	return &SignedProposal{Proposal: proposal, Proposer: addr, Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig}}
}

func signVoteAs(t *testing.T, key *crypto.PrivateKey, addr []byte, vt VoteType, blockHash []byte, round int, height uint64) *SignedVote {
	t.Helper()
	vote := &Vote{BlockHash: blockHash, Round: round, Type: vt, Height: height}
	sig, err := ethSign(sha256Sum(vote.bytes()), key)
	if err != nil {
		t.Fatalf("sign vote: %v", err)
	}
	return &SignedVote{Vote: vote, Validator: addr, Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig}}
}

func headerHash(t *testing.T, block *types.Block) []byte {
	t.Helper()
	hash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}
	return hash
}

// runOneRound runs the engine's real round loop until the round's commit timer
// ends it: every other timer is far away, so the loop does exactly what the
// messages already queued make it do, in order, and nothing else.
func runOneRound(e *Engine) {
	e.proposalTimeout, e.prevoteTimeout, e.precommitTimeout = time.Hour, time.Hour, time.Hour
	e.commitTimeout = 150 * time.Millisecond
	e.runRound()
}

func countType(votes []*SignedVote, vt VoteType) int {
	n := 0
	for _, v := range votes {
		if v.Vote.Type == vt {
			n++
		}
	}
	return n
}

// One byzantine validator that is the round's proposer sends the honest
// validator two proposals for the round. Whatever the order and whatever the
// second one is, the honest validator votes once: its answer to the first stands.
func TestASecondProposalNeverDrawsASecondVoteInTheSameRound(t *testing.T) {
	const round = 4
	cases := []struct {
		name          string
		first, second func(tv *testValidators) *types.Block
		wantNil       bool // the one prevote is for nil (the first proposal was bad)
	}{
		{
			name:   "a good proposal and then a bad one",
			first:  func(tv *testValidators) *types.Block { return goodBlock(round, tv.addrs[1], 0x01) },
			second: func(tv *testValidators) *types.Block { return badBlock(round, tv.addrs[1], 0x01) },
		},
		{
			name:    "a bad proposal and then a good one",
			first:   func(tv *testValidators) *types.Block { return badBlock(round, tv.addrs[1], 0x01) },
			second:  func(tv *testValidators) *types.Block { return goodBlock(round, tv.addrs[1], 0x01) },
			wantNil: true,
		},
		{
			name:   "a good proposal and then a different good one",
			first:  func(tv *testValidators) *types.Block { return goodBlock(round, tv.addrs[1], 0x01) },
			second: func(tv *testValidators) *types.Block { return goodBlock(round, tv.addrs[1], 0x02) },
		},
		{
			name:    "a bad proposal and then a different bad one",
			first:   func(tv *testValidators) *types.Block { return badBlock(round, tv.addrs[1], 0x01) },
			second:  func(tv *testValidators) *types.Block { return badBlock(round, tv.addrs[1], 0x02) },
			wantNil: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tv := validatorsWithProposer(t, 2, round, 1) // validator 1 proposes the round; validator 0 only votes
			engine, node, br := newVoter(t, tv)
			engine.mu.Lock()
			engine.currentState = State{Height: 1, Round: round - 1}
			engine.mu.Unlock()

			firstBlock, secondBlock := tc.first(tv), tc.second(tv)
			if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], firstBlock, round)); err != nil {
				t.Fatalf("first proposal: %v", err)
			}
			if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], secondBlock, round)); err != nil {
				t.Fatalf("second proposal: %v", err)
			}
			runOneRound(engine)

			if got := len(node.validatedHashes()); got != 2 {
				t.Fatalf("test setup: expected both proposals to reach validation, %d did", got)
			}
			votes := br.votes(tv.addrs[0])
			if got := countType(votes, Prevote); got != 1 {
				t.Fatalf("the validator must prevote once in a round, it prevoted %d times: %v", got, describeVotes(votes))
			}
			if got := countType(votes, Precommit); got != 0 {
				t.Fatalf("no precommit is possible here, got %d", got)
			}
			prevote := votes[0]
			if prevote.Vote.Round != round || prevote.Vote.Height != 1 {
				t.Fatalf("the prevote is for height %d round %d, want height 1 round %d", prevote.Vote.Height, prevote.Vote.Round, round)
			}
			wantHash := headerHash(t, firstBlock)
			if tc.wantNil {
				wantHash = nil
			}
			if !bytes.Equal(prevote.Vote.BlockHash, wantHash) {
				t.Fatalf("the vote must be the answer to the FIRST proposal (%x), got %x", wantHash, prevote.Vote.BlockHash)
			}
			requireNoSlashableProof(t, votes, tv.addrs[0])
		})
	}
}

func describeVotes(votes []*SignedVote) string {
	var parts []string
	for _, v := range votes {
		parts = append(parts, fmt.Sprintf("%s(h=%d r=%d %x)", v.Vote.Type, v.Vote.Height, v.Vote.Round, v.Vote.BlockHash))
	}
	return fmt.Sprintf("%v", parts)
}

// A block that passed validation and gathered both quorums but then failed to
// commit used to make the validator sign a prevote for nil after its own prevote
// and precommit for that block, and the reset that followed let a new proposal
// in the same round be prevoted and precommitted again.
func TestAFailedCommitDoesNotLetTheValidatorVoteAgainInThatRound(t *testing.T) {
	const round = 3
	tv := newTestValidators(t, 4)
	engine, node, br := newVoter(t, tv)
	node.commitErr = errors.New("execution failed")
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: round}
	engine.mu.Unlock()

	blockX := candidateBlock(1, round, tv.addrs[1], 0x01)
	hashX := headerHash(t, blockX)
	if !engine.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: blockX, Round: round, ValidRound: -1}, Proposer: tv.addrs[1]}) {
		t.Fatalf("the proposal must be accepted")
	}
	engine.prevote() // the validator prevotes X
	for _, i := range []int{1, 2} {
		engine.addVoteIfRelevant(signVoteAs(t, tv.keys[i], tv.addrs[i], Prevote, hashX, round, 1))
	}
	engine.precommit() // 3 of 4 prevoted X: the validator precommits X
	for _, i := range []int{1, 2} {
		engine.addVoteIfRelevant(signVoteAs(t, tv.keys[i], tv.addrs[i], Precommit, hashX, round, 1))
	}
	if engine.commit() {
		t.Fatalf("test setup: the commit was meant to fail")
	}

	// The failed commit cleared the active proposal: another proposal for the
	// same round is accepted, prevoted by three others and would be precommitted.
	blockY := candidateBlock(1, round, tv.addrs[1], 0x02)
	hashY := headerHash(t, blockY)
	if !engine.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: blockY, Round: round, ValidRound: -1}, Proposer: tv.addrs[1]}) {
		t.Fatalf("test setup: the second proposal must be accepted after the reset")
	}
	engine.prevote()
	for _, i := range []int{1, 2, 3} {
		engine.addVoteIfRelevant(signVoteAs(t, tv.keys[i], tv.addrs[i], Prevote, hashY, round, 1))
	}
	engine.precommit()

	votes := br.votes(tv.addrs[0])
	if len(votes) != 2 || votes[0].Vote.Type != Prevote || votes[1].Vote.Type != Precommit ||
		!bytes.Equal(votes[0].Vote.BlockHash, hashX) || !bytes.Equal(votes[1].Vote.BlockHash, hashX) {
		t.Fatalf("after its prevote and precommit for X the validator may sign nothing else in the round, it broadcast %s", describeVotes(votes))
	}
	requireNoSlashableProof(t, votes, tv.addrs[0])

	// The round is over for this validator, not the height: the next round is
	// voted in normally.
	engine.startNewRound()
	engine.mu.RLock()
	next := engine.currentState.Round
	engine.mu.RUnlock()
	if next != round+1 {
		t.Fatalf("the next round is %d, want %d", next, round+1)
	}
}

// A validator whose commit fails and that has not voted in the round is still
// free to say so with a prevote for nil, as before.
func TestAFailedCommitStillDrawsANilPrevoteWhenTheValidatorHasNotVoted(t *testing.T) {
	tv := newTestValidators(t, 1)
	engine, node, br := newVoter(t, tv)
	node.commitErr = errors.New("execution failed")
	block := candidateBlock(1, 0, tv.addrs[0], 0x01)
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 0}
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: block, Round: 0, ValidRound: -1}, Proposer: tv.addrs[0]}
	engine.receivedVotes[Precommit] = map[string]*SignedVote{string(tv.addrs[0]): {Vote: &Vote{Round: 0, Type: Precommit, Height: 1}, Validator: tv.addrs[0]}}
	engine.receivedPower[Precommit] = big.NewInt(1)
	engine.mu.Unlock()

	if engine.commit() {
		t.Fatalf("test setup: the commit was meant to fail")
	}
	votes := br.votes(tv.addrs[0])
	if len(votes) != 1 || votes[0].Vote.Type != Prevote || len(votes[0].Vote.BlockHash) != 0 {
		t.Fatalf("expected one prevote for nil, got %s", describeVotes(votes))
	}
}

// A validator restarted between voting in a round and committing must not sign
// a different vote for that round, and must not wait through the rounds it
// already voted in: it comes back in the first round it has not voted in.
func TestARestartedValidatorNeverSignsAgainInARoundItVotedIn(t *testing.T) {
	const votedRound = 5
	path := filepath.Join(t.TempDir(), "bft_sign_state.json")
	tv := newTestValidators(t, 2)
	node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}

	// Before the restart: the validator prevotes block X in round 5.
	brBefore := &captureBroadcaster{}
	before := NewEngine(node, tv.keys[0], brBefore, WithSignStatePath(path))
	before.mu.Lock()
	before.currentState = State{Height: 1, Round: votedRound}
	before.mu.Unlock()
	blockX := candidateBlock(1, votedRound, tv.addrs[1], 0x01)
	if !before.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: blockX, Round: votedRound, ValidRound: -1}, Proposer: tv.addrs[1]}) {
		t.Fatalf("the proposal must be accepted")
	}
	before.prevote()
	preVotes := brBefore.votes(tv.addrs[0])
	if len(preVotes) != 1 {
		t.Fatalf("test setup: expected one prevote before the restart, got %d", len(preVotes))
	}

	// The restart: a new engine on the same node (no block committed) and the
	// same record. Put in round 5 by hand it must refuse a different block ...
	brSame := &captureBroadcaster{}
	restarted := NewEngine(node, tv.keys[0], brSame, WithSignStatePath(path))
	restarted.mu.Lock()
	restarted.currentState = State{Height: 1, Round: votedRound}
	restarted.mu.Unlock()
	blockY := candidateBlock(1, votedRound, tv.addrs[1], 0x02)
	if !restarted.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: blockY, Round: votedRound, ValidRound: -1}, Proposer: tv.addrs[1]}) {
		t.Fatalf("the proposal must be accepted")
	}
	restarted.prevote()
	if votes := brSame.votes(tv.addrs[0]); len(votes) != 0 {
		t.Fatalf("a restarted validator signed a different vote for the round it had already voted in: %s", describeVotes(votes))
	}

	// ... and started the way a restart does, at the first round of the height,
	// it goes straight to the first round it has not voted in ...
	brNext := &captureBroadcaster{}
	fresh := NewEngine(node, tv.keys[0], brNext, WithSignStatePath(path))
	fresh.startNewRound()
	fresh.mu.RLock()
	round := fresh.currentState.Round
	fresh.mu.RUnlock()
	if round != votedRound+1 {
		t.Fatalf("the restarted validator starts round %d, want %d (the first round after the one it voted in)", round, votedRound+1)
	}

	// ... where it votes normally.
	blockZ := candidateBlock(1, round, tv.addrs[1], 0x03)
	if !fresh.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: blockZ, Round: round, ValidRound: -1}, Proposer: tv.addrs[1]}) {
		t.Fatalf("the proposal must be accepted")
	}
	fresh.prevote()
	nextVotes := brNext.votes(tv.addrs[0])
	if len(nextVotes) != 1 || !bytes.Equal(nextVotes[0].Vote.BlockHash, headerHash(t, blockZ)) || nextVotes[0].Vote.Round != round {
		t.Fatalf("the restarted validator must vote in the next round, it broadcast %s", describeVotes(nextVotes))
	}

	requireNoSlashableProof(t, append(append(preVotes, brSame.votes(tv.addrs[0])...), nextVotes...), tv.addrs[0])
}

// The record of a vote is on disk before the vote leaves the process, so a
// crash after the vote is sent can never have lost it. The check reads the file
// at the moment each vote is broadcast: a prevote for a block, a precommit for
// it, and a prevote for nil in a later round.
func TestTheVoteRecordIsDurableBeforeTheVoteIsBroadcast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bft_sign_state.json")
	tv := newTestValidators(t, 2) // two validators: this one's vote alone is never a quorum
	engine, _, br := newVoter(t, tv, WithSignStatePath(path))

	type slot struct {
		BlockHash []byte `json:"blockHash"`
	}
	type record struct {
		Height    uint64 `json:"height"`
		Round     int    `json:"round"`
		Prevote   *slot  `json:"prevote"`
		Precommit *slot  `json:"precommit"`
	}
	var broadcast []string
	br.onVote = func(sv *SignedVote) {
		broadcast = append(broadcast, sv.Vote.Type.String())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("a %s was broadcast before its record was written: %v", sv.Vote.Type, err)
			return
		}
		var rec record
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Errorf("the record is not readable when a %s is broadcast: %v", sv.Vote.Type, err)
			return
		}
		if rec.Height != sv.Vote.Height || rec.Round != sv.Vote.Round {
			t.Errorf("a %s for height %d round %d was broadcast while the record says height %d round %d", sv.Vote.Type, sv.Vote.Height, sv.Vote.Round, rec.Height, rec.Round)
			return
		}
		held := rec.Prevote
		if sv.Vote.Type == Precommit {
			held = rec.Precommit
		}
		if held == nil || !bytes.Equal(held.BlockHash, sv.Vote.BlockHash) {
			t.Errorf("a %s for %x was broadcast before the record held it: %s", sv.Vote.Type, sv.Vote.BlockHash, raw)
		}
	}

	block := candidateBlock(1, 2, tv.addrs[1], 0x01)
	hash := headerHash(t, block)
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 2}
	engine.mu.Unlock()
	engine.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: block, Round: 2, ValidRound: -1}, Proposer: tv.addrs[1]})
	engine.prevote()
	engine.addVoteIfRelevant(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hash, 2, 1)) // the other validator prevotes: a quorum
	engine.precommit()
	if len(broadcast) != 2 {
		t.Fatalf("expected a prevote and a precommit to be broadcast, saw %v", broadcast)
	}

	// A prevote for nil in a later round.
	engine.mu.Lock()
	engine.currentState.Round = 3
	engine.broadcastPrevoteNilLocked("test")
	engine.mu.Unlock()
	if len(broadcast) != 3 {
		t.Fatalf("expected the nil prevote to be broadcast too, saw %v", broadcast)
	}
}

// meshBroadcaster delivers what an engine sends to its peer engine, one
// goroutine per message like a real network, and records it. Once dead it
// delivers and records nothing more: the process is gone.
type meshBroadcaster struct {
	mu       sync.Mutex
	peer     *Engine
	peerNode *signGuardNode // where a committed block sent to the peer is applied
	msgs     []*p2p.Message
	dead     bool
}

func (b *meshBroadcaster) Broadcast(msg *p2p.Message) error {
	b.mu.Lock()
	if b.dead {
		b.mu.Unlock()
		return nil
	}
	b.msgs = append(b.msgs, msg)
	peer, peerNode := b.peer, b.peerNode
	b.mu.Unlock()
	if peer == nil {
		return nil
	}
	payload := append([]byte(nil), msg.Payload...)
	kind := msg.Type
	go func() {
		switch kind {
		case p2p.MsgTypeProposal:
			var sp SignedProposal
			if json.Unmarshal(payload, &sp) == nil {
				peer.HandleProposal(&sp)
			}
		case p2p.MsgTypeVote:
			var sv SignedVote
			if json.Unmarshal(payload, &sv) == nil {
				peer.HandleVote(&sv)
			}
		case p2p.MsgTypeBlock:
			// A committed block is how production brings a lagging peer up to date
			// (a validator whose round moved on before the last precommit reached it
			// would otherwise never see the height it just helped commit).
			var block types.Block
			if json.Unmarshal(payload, &block) == nil && peerNode != nil {
				peerNode.applySynced(&block)
				peer.NotifyExternalCommit()
			}
		}
	}()
	return nil
}

func (b *meshBroadcaster) setPeer(peer *Engine, peerNode *signGuardNode) {
	b.mu.Lock()
	b.peer, b.peerNode = peer, peerNode
	b.mu.Unlock()
}

func (b *meshBroadcaster) kill() {
	b.mu.Lock()
	b.dead = true
	b.mu.Unlock()
}

func (b *meshBroadcaster) messages() []*p2p.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*p2p.Message(nil), b.msgs...)
}

// The whole scenario on two real engines over real messages and real timers:
// the round's proposer proposes a block and prevotes it, then its process dies
// before the round completes and is started again. The restarted proposer would
// build a different block for the same round and sign it; it must not, and the
// two validators must still commit the height.
func TestARestartedProposerNeverDoubleSignsAndTheChainStillCommits(t *testing.T) {
	// The first round a fresh engine runs is round 1; make validator 0 its proposer.
	tv := validatorsWithProposer(t, 2, 1, 0)
	nodeV := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}
	nodeW := &signGuardNode{validatorSet: tv.set, self: tv.addrs[1]}
	path := filepath.Join(t.TempDir(), "bft_sign_state.json")
	slow := TimeoutConfig{Proposal: time.Hour, Prevote: time.Hour, Precommit: time.Hour, Commit: time.Hour}
	fast := TimeoutConfig{Proposal: 30 * time.Millisecond, Prevote: 30 * time.Millisecond, Precommit: 30 * time.Millisecond, Commit: 30 * time.Millisecond}

	brW := &meshBroadcaster{}
	engineW := NewEngine(nodeW, tv.keys[1], brW, WithTimeouts(fast))

	// Before the crash: the proposer runs alone, proposes and prevotes.
	brV1 := &meshBroadcaster{peer: engineW, peerNode: nodeW}
	engineV1 := NewEngine(nodeV, tv.keys[0], brV1, WithSignStatePath(path), WithTimeouts(slow))
	brW.setPeer(engineV1, nodeV)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		engineV1.runRound()
	}()
	deadline := time.Now().Add(5 * time.Second)
	var preCrashVotes []*SignedVote
	for time.Now().Before(deadline) {
		if preCrashVotes = votesBy(brV1.messages(), tv.addrs[0]); len(preCrashVotes) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(preCrashVotes) != 1 || preCrashVotes[0].Vote.Type != Prevote || preCrashVotes[0].Vote.Round != 1 {
		t.Fatalf("test setup: the proposer should have prevoted once, in round 1, before the crash: %s", describeVotes(preCrashVotes))
	}
	// The crash: nothing it sends from now on leaves the process, and its loop ends.
	brV1.kill()
	engineV1.NotifyExternalCommit()
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("test setup: the first engine did not stop")
	}

	// The restart: a new engine on the same node and the same record, the other
	// validator still running.
	brV2 := &meshBroadcaster{peer: engineW, peerNode: nodeW}
	engineV2 := NewEngine(nodeV, tv.keys[0], brV2, WithSignStatePath(path), WithTimeouts(fast))
	brW.setPeer(engineV2, nodeV)

	run := func(e *Engine, n *signGuardNode) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; i < 400 && n.GetHeight() < 1; i++ {
				e.runRound()
			}
		}()
		return done
	}
	doneW, doneV := run(engineW, nodeW), run(engineV2, nodeV)
	timeout := time.After(20 * time.Second)
	for doneW != nil || doneV != nil {
		select {
		case <-doneW:
			doneW = nil
		case <-doneV:
			doneV = nil
		case <-timeout:
			t.Fatalf("LIVENESS: the validators did not commit height 1 after the restart (proposer height %d, other height %d)", nodeV.GetHeight(), nodeW.GetHeight())
		}
	}

	if nodeV.GetHeight() < 1 || nodeW.GetHeight() < 1 {
		t.Fatalf("both validators must commit height 1, got %d and %d", nodeV.GetHeight(), nodeW.GetHeight())
	}
	if hv, hw := nodeV.committedHash(t), nodeW.committedHash(t); !bytes.Equal(hv, hw) {
		t.Fatalf("SAFETY: the validators committed different blocks at height 1: %x and %x", hv, hw)
	}
	all := append(append([]*SignedVote(nil), preCrashVotes...), votesBy(brV2.messages(), tv.addrs[0])...)
	requireNoSlashableProof(t, all, tv.addrs[0])
	for _, v := range votesBy(brV2.messages(), tv.addrs[0]) {
		if v.Vote.Height == 1 && v.Vote.Round <= 1 {
			t.Fatalf("the restarted validator signed a %s in round %d, a round it had already voted in", v.Vote.Type, v.Vote.Round)
		}
	}
}
