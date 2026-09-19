package bft

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// livenessNode is a NodeInterface double that records what the engine asks of
// it and can be told to fail its own validation, and that implements the
// optional proposalFeedback extension.
type livenessNode struct {
	validatorSet map[string]*big.Int
	validator    []byte
	height       uint64

	mempool      []*types.Transaction
	createArgs   [][]*types.Transaction
	createErr    error
	validateErrs []error // consumed one per ValidateBlock call, then nil
	requeued     []*types.Transaction
	outcomes     []outcomeRecord
}

type outcomeRecord struct {
	height  uint64
	round   int
	kind    string
	txCount int
	err     error
}

func (n *livenessNode) GetMempool() []*types.Transaction { return n.mempool }
func (n *livenessNode) RequeueTransactions(txs []*types.Transaction) {
	n.requeued = append(n.requeued, txs...)
}
func (n *livenessNode) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	n.createArgs = append(n.createArgs, txs)
	if n.createErr != nil {
		return nil, n.createErr
	}
	return types.NewBlock(&types.BlockHeader{Height: n.height + 1, Validator: n.validator}, txs), nil
}
func (n *livenessNode) ValidateBlock(*types.Block) error {
	if len(n.validateErrs) == 0 {
		return nil
	}
	err := n.validateErrs[0]
	n.validateErrs = n.validateErrs[1:]
	return err
}
func (n *livenessNode) CommitBlock(*types.Block) error       { return nil }
func (n *livenessNode) GetValidatorSet() map[string]*big.Int { return n.validatorSet }
func (n *livenessNode) GetAccount([]byte) (*types.Account, error) {
	return &types.Account{Stake: big.NewInt(1)}, nil
}
func (n *livenessNode) GetLastCommitHash() []byte { return nil }
func (n *livenessNode) GetHeight() uint64         { return n.height }
func (n *livenessNode) NoteProposalOutcome(height uint64, round int, kind string, txCount int, err error) {
	n.outcomes = append(n.outcomes, outcomeRecord{height, round, kind, txCount, err})
}

func newLivenessEngine(t *testing.T, node *livenessNode) (*Engine, []byte) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().Bytes()
	node.validator = addr
	node.validatorSet = map[string]*big.Int{string(addr): big.NewInt(1)}
	return NewEngine(node, key, &recordingBroadcaster{}), addr
}

func testTxs(n int) []*types.Transaction {
	txs := make([]*types.Transaction, n)
	for i := range txs {
		txs[i] = &types.Transaction{Type: types.TxTypeTransfer, Nonce: uint64(i)}
	}
	return txs
}

// After two of its own proposals at a height fail to commit, a proposer stops
// offering transactions for that height and proposes an empty block, which
// depends on no transaction at all -- so a transaction some other validator
// rejects can cost a couple of rounds but never stall the chain.
func TestProposeFallsBackToAnEmptyBlockAfterTwoFailedOwnProposals(t *testing.T) {
	txs := testTxs(3)
	node := &livenessNode{mempool: txs, validateErrs: []error{
		errors.New("own validation failed once"), errors.New("own validation failed twice"),
	}}
	engine, _ := newLivenessEngine(t, node)

	for i := 1; i <= 2; i++ {
		err := engine.propose()
		if err == nil {
			t.Fatalf("attempt %d: expected the local validation failure to be reported", i)
		}
	}
	if got := len(node.createArgs); got != 2 || len(node.createArgs[0]) != 3 || len(node.createArgs[1]) != 3 {
		t.Fatalf("the first two attempts offer the mempool, got %d builds", got)
	}
	// Each failed local validation releases the block's transactions (nothing
	// else would: the block is never proposed, so nothing requeues it).
	if len(node.requeued) != 6 {
		t.Fatalf("expected the 3 transactions requeued after each of the 2 failures, got %d", len(node.requeued))
	}
	if got := engine.ownFailedAt(1); got != 2 {
		t.Fatalf("expected 2 own failures at height 1, got %d", got)
	}
	node.requeued = nil

	if err := engine.propose(); err != nil {
		t.Fatalf("third attempt: %v", err)
	}
	if got := len(node.createArgs); got != 3 || node.createArgs[2] != nil {
		t.Fatalf("after two failures the proposal must be built from NO transactions, got %v", node.createArgs[len(node.createArgs)-1])
	}
	if len(node.requeued) != 3 {
		t.Fatalf("the offered transactions are released, not dropped: got %d requeued", len(node.requeued))
	}
	engine.mu.RLock()
	block := engine.activeProposal.Proposal.Block
	engine.mu.RUnlock()
	if len(block.Transactions) != 0 {
		t.Fatalf("the proposed block must be empty, got %d transactions", len(block.Transactions))
	}
	// The node was told about both local validation failures.
	kinds := 0
	for _, o := range node.outcomes {
		if o.kind == outcomeLocalValidationFailed && o.height == 1 && o.txCount == 3 {
			kinds++
		}
	}
	if kinds != 2 {
		t.Fatalf("expected two local_validation_failed outcomes, got %+v", node.outcomes)
	}
}

func TestEmptyBlockFallbackCanBeDisabled(t *testing.T) {
	t.Setenv(emptyAfterEnv, "0")
	node := &livenessNode{mempool: testTxs(2), validateErrs: []error{errors.New("a"), errors.New("b")}}
	engine, _ := newLivenessEngine(t, node)
	_ = engine.propose()
	_ = engine.propose()
	if err := engine.propose(); err != nil {
		t.Fatalf("third attempt: %v", err)
	}
	if last := node.createArgs[len(node.createArgs)-1]; len(last) != 2 {
		t.Fatalf("with the fallback disabled the mempool is still offered, got %d", len(last))
	}
}

func TestEmptyAfterEnvironmentOverride(t *testing.T) {
	t.Setenv(emptyAfterEnv, "5")
	if got := emptyAfterFromEnv(); got != 5 {
		t.Fatalf("override = %d", got)
	}
	t.Setenv(emptyAfterEnv, "-1")
	if got := emptyAfterFromEnv(); got != defaultEmptyAfterFailures {
		t.Fatalf("a malformed value falls back to the default, got %d", got)
	}
	t.Setenv(emptyAfterEnv, "")
	if got := emptyAfterFromEnv(); got != defaultEmptyAfterFailures {
		t.Fatalf("unset = %d", got)
	}
}

// A build error is also one of the proposer's own failed proposals.
func TestBuildErrorCountsAsAnOwnFailureAndIsReported(t *testing.T) {
	node := &livenessNode{mempool: testTxs(2), createErr: errors.New("cannot build")}
	engine, _ := newLivenessEngine(t, node)
	if err := engine.propose(); err == nil {
		t.Fatalf("expected the build error")
	}
	if got := engine.ownFailedAt(1); got != 1 {
		t.Fatalf("expected 1 own failure, got %d", got)
	}
	if len(node.outcomes) != 1 || node.outcomes[0].kind != outcomeBuildFailed {
		t.Fatalf("expected a build_failed outcome, got %+v", node.outcomes)
	}
}

// A round that ends with this validator's own proposal uncommitted counts as a
// failure; a peer's proposal that fails does not, and counts for a height that
// has been left behind are forgotten.
func TestStartNewRoundCountsOnlyOwnUncommittedProposals(t *testing.T) {
	node := &livenessNode{}
	engine, self := newLivenessEngine(t, node)
	other := []byte("some other validator")

	proposal := func(proposer []byte, height uint64) *SignedProposal {
		return &SignedProposal{
			Proposer: proposer,
			Proposal: &Proposal{Block: types.NewBlock(&types.BlockHeader{Height: height, Validator: proposer}, testTxs(2)), Round: 0},
		}
	}

	engine.mu.Lock()
	engine.activeProposal = proposal(other, 1)
	engine.mu.Unlock()
	engine.startNewRound()
	if got := engine.ownFailedAt(1); got != 0 {
		t.Fatalf("a peer's failed proposal is not this validator's failure, got %d", got)
	}

	engine.mu.Lock()
	engine.activeProposal = proposal(self, 1)
	engine.mu.Unlock()
	engine.startNewRound()
	if got := engine.ownFailedAt(1); got != 1 {
		t.Fatalf("own uncommitted proposal must count once, got %d", got)
	}
	if len(node.outcomes) != 1 || node.outcomes[0].kind != outcomeNotCommitted || node.outcomes[0].txCount != 2 {
		t.Fatalf("expected a not_committed outcome, got %+v", node.outcomes)
	}

	// The chain moves on: the old height's count is forgotten.
	node.height = 1
	engine.startNewRound()
	if got := engine.ownFailedAt(1); got != 0 {
		t.Fatalf("counts for finished heights must be pruned, got %d", got)
	}
}

func TestEngineStatusReportsHeightRoundAndLock(t *testing.T) {
	node := &livenessNode{}
	engine, _ := newLivenessEngine(t, node)
	height, round, locked := engine.Status()
	if height != 1 || round != 0 || locked != -1 {
		t.Fatalf("Status = (%d, %d, %d), want (1, 0, -1)", height, round, locked)
	}
	engine.mu.Lock()
	engine.currentState.Round = 3
	engine.lockedRound = 2
	engine.mu.Unlock()
	if height, round, locked = engine.Status(); height != 1 || round != 3 || locked != 2 {
		t.Fatalf("Status = (%d, %d, %d), want (1, 3, 2)", height, round, locked)
	}
	var none *Engine
	if h, r, l := none.Status(); h != 0 || r != 0 || l != -1 {
		t.Fatalf("a nil engine reports zeroes and no lock")
	}
}

// The extension is optional: a node that does not implement it (every other
// test double) must keep working.
func TestProposalFeedbackIsOptional(t *testing.T) {
	node := &requeueTrackingNode{validatorSet: map[string]*big.Int{}}
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	node.validatorSet[string(key.PubKey().Address().Bytes())] = big.NewInt(1)
	engine := NewEngine(node, key, &recordingBroadcaster{})
	engine.noteOwnFailure(1, 0, outcomeBuildFailed, 0, errors.New("x")) // must not panic
	if got := engine.ownFailedAt(1); got != 1 {
		t.Fatalf("failure not counted for a node without the feedback extension: %d", got)
	}
}
