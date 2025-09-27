package bft

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"
)

type failingNode struct {
	validatorSet map[string]*big.Int
	commitErr    error
	height       uint64
}

func (n *failingNode) GetMempool() []*types.Transaction { return nil }
func (n *failingNode) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	return nil, nil
}
func (n *failingNode) CommitBlock(block *types.Block) error { return n.commitErr }
func (n *failingNode) GetValidatorSet() map[string]*big.Int { return n.validatorSet }
func (n *failingNode) GetAccount(addr []byte) (*types.Account, error) {
	return &types.Account{Stake: big.NewInt(1)}, nil
}
func (n *failingNode) GetLastCommitHash() []byte { return nil }
func (n *failingNode) GetHeight() uint64         { return n.height }

type recordingBroadcaster struct {
	messages []*p2p.Message
}

func (r *recordingBroadcaster) Broadcast(msg *p2p.Message) error {
	r.messages = append(r.messages, msg)
	return nil
}

type trackingNode struct {
	validatorSet map[string]*big.Int
	committed    []*types.Block
	height       uint64
}

func (n *trackingNode) GetMempool() []*types.Transaction { return nil }
func (n *trackingNode) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	return nil, nil
}
func (n *trackingNode) CommitBlock(block *types.Block) error {
	n.committed = append(n.committed, block)
	if block != nil && block.Header != nil {
		n.height = block.Header.Height
	}
	return nil
}
func (n *trackingNode) GetValidatorSet() map[string]*big.Int { return n.validatorSet }
func (n *trackingNode) GetAccount(addr []byte) (*types.Account, error) {
	weight := n.validatorSet[string(addr)]
	if weight == nil {
		weight = big.NewInt(0)
	}
	return &types.Account{Stake: new(big.Int).Set(weight)}, nil
}
func (n *trackingNode) GetLastCommitHash() []byte { return nil }
func (n *trackingNode) GetHeight() uint64         { return n.height }

type emptyBlockNode struct {
	validatorSet map[string]*big.Int
	committed    []*types.Block
	height       uint64
	validator    []byte
}

func (n *emptyBlockNode) GetMempool() []*types.Transaction { return nil }
func (n *emptyBlockNode) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	header := &types.BlockHeader{
		Height:    n.height + 1,
		Validator: n.validator,
	}
	return types.NewBlock(header, txs), nil
}
func (n *emptyBlockNode) CommitBlock(block *types.Block) error {
	n.committed = append(n.committed, block)
	if block != nil && block.Header != nil {
		n.height = block.Header.Height
	}
	return nil
}
func (n *emptyBlockNode) GetValidatorSet() map[string]*big.Int { return n.validatorSet }
func (n *emptyBlockNode) GetAccount(addr []byte) (*types.Account, error) {
	weight := n.validatorSet[string(addr)]
	if weight == nil {
		weight = big.NewInt(0)
	}
	return &types.Account{Stake: new(big.Int).Set(weight)}, nil
}
func (n *emptyBlockNode) GetLastCommitHash() []byte { return nil }
func (n *emptyBlockNode) GetHeight() uint64         { return n.height }

func TestCommitBroadcastsPrevoteNilOnExecutionFailure(t *testing.T) {
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	validatorAddr := validatorKey.PubKey().Address().Bytes()

	node := &failingNode{
		validatorSet: map[string]*big.Int{string(validatorAddr): big.NewInt(1)},
		commitErr:    errors.New("execution failed"),
	}
	broadcaster := &recordingBroadcaster{}

	engine := NewEngine(node, validatorKey, broadcaster)

	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 0}
	engine.activeProposal = &SignedProposal{
		Proposal: &Proposal{
			Block: types.NewBlock(&types.BlockHeader{Height: 1}, nil),
			Round: 0,
		},
	}
	engine.receivedVotes[Precommit] = map[string]*SignedVote{
		string(validatorAddr): {
			Vote: &Vote{
				Round:  0,
				Type:   Precommit,
				Height: 1,
			},
			Validator: validatorAddr,
		},
	}
	engine.receivedPower[Precommit] = new(big.Int).Set(node.validatorSet[string(validatorAddr)])
	engine.mu.Unlock()

	engine.commit()

	engine.mu.Lock()
	defer engine.mu.Unlock()

	if engine.activeProposal != nil {
		t.Fatalf("expected active proposal to be cleared after execution failure")
	}
	if len(broadcaster.messages) != 1 {
		t.Fatalf("expected 1 broadcasted message, got %d", len(broadcaster.messages))
	}
	msg := broadcaster.messages[0]
	if msg.Type != p2p.MsgTypeVote {
		t.Fatalf("expected vote message, got %d", msg.Type)
	}
	var signedVote SignedVote
	if err := json.Unmarshal(msg.Payload, &signedVote); err != nil {
		t.Fatalf("unmarshal vote: %v", err)
	}
	if signedVote.Vote == nil {
		t.Fatalf("expected vote payload to be populated")
	}
	if signedVote.Vote.Type != Prevote {
		t.Fatalf("expected prevote type, got %v", signedVote.Vote.Type)
	}
	if len(signedVote.Vote.BlockHash) != 0 {
		t.Fatalf("expected nil block hash in prevote, got %x", signedVote.Vote.BlockHash)
	}
	if len(engine.receivedVotes[Prevote]) != 0 {
		t.Fatalf("expected prevote records to be cleared after failure")
	}
	if engine.committedBlocks[engine.currentState.Height] {
		t.Fatalf("block should not be marked committed on execution failure")
	}
}

func TestProposeAllowsEmptyMempool(t *testing.T) {
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	validatorAddr := validatorKey.PubKey().Address().Bytes()

	node := &emptyBlockNode{
		validatorSet: map[string]*big.Int{string(validatorAddr): big.NewInt(1)},
		validator:    validatorAddr,
	}
	broadcaster := &recordingBroadcaster{}

	engine := NewEngine(node, validatorKey, broadcaster)

	if err := engine.propose(); err != nil {
		t.Fatalf("propose with empty mempool: %v", err)
	}

	engine.mu.RLock()
	if engine.activeProposal == nil || engine.activeProposal.Proposal == nil || engine.activeProposal.Proposal.Block == nil {
		engine.mu.RUnlock()
		t.Fatalf("expected active proposal with block to be set")
	}
	block := engine.activeProposal.Proposal.Block
	engine.mu.RUnlock()

	if block.Header == nil {
		t.Fatalf("expected proposed block to include header")
	}
	if len(block.Transactions) != 0 {
		t.Fatalf("expected empty transaction set, got %d", len(block.Transactions))
	}

	engine.prevote()
	engine.precommit()

	if node.height == 0 {
		if !engine.commit() {
			t.Fatalf("expected commit to succeed for empty block")
		}
	}

	if node.height != 1 {
		t.Fatalf("expected node height to advance to 1, got %d", node.height)
	}
	if len(node.committed) != 1 {
		t.Fatalf("expected exactly one committed block, got %d", len(node.committed))
	}
	if len(node.committed[0].Transactions) != 0 {
		t.Fatalf("expected committed block to contain zero transactions")
	}
}

func TestCommitBroadcastsPrevoteNilOnTimestampViolation(t *testing.T) {
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	validatorAddr := validatorKey.PubKey().Address().Bytes()

	node := &failingNode{
		validatorSet: map[string]*big.Int{string(validatorAddr): big.NewInt(1)},
		commitErr:    errors.New("block timestamp outside allowed window"),
	}
	broadcaster := &recordingBroadcaster{}

	engine := NewEngine(node, validatorKey, broadcaster)

	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 0}
	engine.activeProposal = &SignedProposal{
		Proposal: &Proposal{
			Block: types.NewBlock(&types.BlockHeader{Height: 1}, nil),
			Round: 0,
		},
	}
	engine.receivedVotes[Precommit] = map[string]*SignedVote{
		string(validatorAddr): {
			Vote: &Vote{
				Round:  0,
				Type:   Precommit,
				Height: 1,
			},
			Validator: validatorAddr,
		},
	}
	engine.receivedPower[Precommit] = new(big.Int).Set(node.validatorSet[string(validatorAddr)])
	engine.mu.Unlock()

	engine.commit()

	engine.mu.Lock()
	defer engine.mu.Unlock()

	if engine.activeProposal != nil {
		t.Fatalf("expected active proposal to be cleared after timestamp violation")
	}
	if len(broadcaster.messages) != 1 {
		t.Fatalf("expected 1 broadcasted message, got %d", len(broadcaster.messages))
	}
	msg := broadcaster.messages[0]
	if msg.Type != p2p.MsgTypeVote {
		t.Fatalf("expected vote message, got %d", msg.Type)
	}
	var signedVote SignedVote
	if err := json.Unmarshal(msg.Payload, &signedVote); err != nil {
		t.Fatalf("unmarshal vote: %v", err)
	}
	if signedVote.Vote == nil || signedVote.Vote.Type != Prevote {
		t.Fatalf("expected prevote in response to timestamp violation")
	}
	if len(signedVote.Vote.BlockHash) != 0 {
		t.Fatalf("expected nil block hash in prevote, got %x", signedVote.Vote.BlockHash)
	}
	if len(engine.receivedVotes[Prevote]) != 0 {
		t.Fatalf("expected prevote records to be cleared after timestamp violation")
	}
	if engine.committedBlocks[engine.currentState.Height] {
		t.Fatalf("block should not be marked committed on timestamp violation")
	}
}

func TestAddVoteIfRelevantUsesVotingPower(t *testing.T) {
	keyA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key A: %v", err)
	}
	keyB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key B: %v", err)
	}
	keyC, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key C: %v", err)
	}

	addrA := keyA.PubKey().Address().Bytes()
	addrB := keyB.PubKey().Address().Bytes()
	addrC := keyC.PubKey().Address().Bytes()

	weights := map[string]*big.Int{
		string(addrA): big.NewInt(5),
		string(addrB): big.NewInt(3),
		string(addrC): big.NewInt(2),
	}

	node := &trackingNode{validatorSet: weights}
	engine := NewEngine(node, keyA, &recordingBroadcaster{})

	block := types.NewBlock(&types.BlockHeader{Height: 1, Validator: addrA}, nil)

	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 0}
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: block, Round: 0}}
	engine.resetVoteTrackingLocked()
	engine.mu.Unlock()

	blockHash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}

	added, reachedPrevote, reachedPrecommit := engine.addVoteIfRelevant(&SignedVote{
		Vote:      &Vote{BlockHash: blockHash, Round: 0, Type: Prevote, Height: 1},
		Validator: addrA,
	})
	if !added {
		t.Fatalf("expected to record validator A prevote")
	}
	if reachedPrevote {
		t.Fatalf("prevote quorum should require more power")
	}
	if reachedPrecommit {
		t.Fatalf("precommit quorum should not trigger on prevote")
	}

	added, reachedPrevote, _ = engine.addVoteIfRelevant(&SignedVote{
		Vote:      &Vote{BlockHash: blockHash, Round: 0, Type: Prevote, Height: 1},
		Validator: addrB,
	})
	if !added {
		t.Fatalf("expected to record validator B prevote")
	}
	if !reachedPrevote {
		t.Fatalf("expected prevote quorum once power exceeds two-thirds")
	}

	added, _, _ = engine.addVoteIfRelevant(&SignedVote{
		Vote:      &Vote{BlockHash: blockHash, Round: 0, Type: Prevote, Height: 1},
		Validator: addrB,
	})
	if added {
		t.Fatalf("duplicate prevote should not be accepted")
	}

	added, _, reachedPrecommit = engine.addVoteIfRelevant(&SignedVote{
		Vote:      &Vote{BlockHash: blockHash, Round: 0, Type: Precommit, Height: 1},
		Validator: addrA,
	})
	if !added {
		t.Fatalf("expected to record validator A precommit")
	}
	if reachedPrecommit {
		t.Fatalf("precommit quorum should require additional power")
	}

	_, _, reachedPrecommit = engine.addVoteIfRelevant(&SignedVote{
		Vote:      &Vote{BlockHash: blockHash, Round: 0, Type: Precommit, Height: 1},
		Validator: addrB,
	})
	if !reachedPrecommit {
		t.Fatalf("expected precommit quorum once power exceeds two-thirds")
	}
}

func TestCommitSucceedsWithWeightedQuorum(t *testing.T) {
	keyA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key A: %v", err)
	}
	keyB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key B: %v", err)
	}
	keyC, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key C: %v", err)
	}

	addrA := keyA.PubKey().Address().Bytes()
	addrB := keyB.PubKey().Address().Bytes()
	addrC := keyC.PubKey().Address().Bytes()

	weights := map[string]*big.Int{
		string(addrA): big.NewInt(5),
		string(addrB): big.NewInt(3),
		string(addrC): big.NewInt(2),
	}

	node := &trackingNode{validatorSet: weights}
	engine := NewEngine(node, keyA, &recordingBroadcaster{})

	block := types.NewBlock(&types.BlockHeader{Height: 1, Validator: addrA}, nil)
	blockHash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}

	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 0}
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: block, Round: 0}}
	engine.resetVoteTrackingLocked()
	engine.mu.Unlock()

	// Precommit power should not reach quorum with only validator A.
	added, _, reachedPrecommit := engine.addVoteIfRelevant(&SignedVote{
		Vote:      &Vote{BlockHash: blockHash, Round: 0, Type: Precommit, Height: 1},
		Validator: addrA,
	})
	if !added {
		t.Fatalf("expected to record validator A precommit")
	}
	if reachedPrecommit {
		t.Fatalf("precommit quorum should require more than one validator")
	}

	if engine.commit() {
		t.Fatalf("commit should fail without two-thirds voting power")
	}

	// Adding validator B's vote should satisfy the weighted quorum (5 + 3 > 2/3 * 10).
	_, _, reachedPrecommit = engine.addVoteIfRelevant(&SignedVote{
		Vote:      &Vote{BlockHash: blockHash, Round: 0, Type: Precommit, Height: 1},
		Validator: addrB,
	})
	if !reachedPrecommit {
		t.Fatalf("expected precommit quorum with validators A and B")
	}

	if !engine.commit() {
		t.Fatalf("expected commit to succeed once weighted quorum is reached")
	}
	if len(node.committed) != 1 {
		t.Fatalf("expected exactly one block to be committed, got %d", len(node.committed))
	}
	if node.committed[0].Header.Height != 1 {
		t.Fatalf("expected committed block height 1, got %d", node.committed[0].Header.Height)
	}
	if engine.currentState.Height != 2 {
		t.Fatalf("expected engine to advance to height 2, got %d", engine.currentState.Height)
	}
	if engine.activeProposal != nil {
		t.Fatalf("expected active proposal to be cleared after commit")
	}
}

func TestEngineFastForwardsHeightOnRestart(t *testing.T) {
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	validatorAddr := validatorKey.PubKey().Address().Bytes()

	initialHeight := uint64(5)
	node := &trackingNode{
		validatorSet: map[string]*big.Int{string(validatorAddr): big.NewInt(1)},
		height:       initialHeight,
	}
	engine := NewEngine(node, validatorKey, &recordingBroadcaster{})

	engine.mu.Lock()
	engine.currentState = State{Height: 2, Round: 3}
	engine.committedBlocks[1] = true
	engine.mu.Unlock()

	engine.startNewRound()

	engine.mu.RLock()
	syncedHeight := engine.currentState.Height
	syncedRound := engine.currentState.Round
	_, stalePresent := engine.committedBlocks[1]
	engine.mu.RUnlock()

	expectedHeight := initialHeight + 1
	if syncedHeight != expectedHeight {
		t.Fatalf("expected engine height %d after sync, got %d", expectedHeight, syncedHeight)
	}
	if syncedRound != 0 {
		t.Fatalf("expected engine round 0 after height sync, got %d", syncedRound)
	}
	if stalePresent {
		t.Fatalf("expected committedBlocks to be pruned for stale heights")
	}

	block := types.NewBlock(&types.BlockHeader{Height: syncedHeight, Validator: validatorAddr}, nil)
	proposal := &Proposal{Block: block, Round: syncedRound}
	hash := sha256.Sum256(proposal.bytes())
	sig, err := ethcrypto.Sign(hash[:], validatorKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign proposal: %v", err)
	}
	signedProposal := &SignedProposal{
		Proposal: proposal,
		Proposer: validatorAddr,
		Signature: &Signature{
			Scheme:    SignatureSchemeSecp256k1,
			Signature: sig,
		},
	}

	if err := engine.HandleProposal(signedProposal); err != nil {
		t.Fatalf("handle proposal: %v", err)
	}

	select {
	case got := <-engine.proposalCh:
		if got != signedProposal {
			t.Fatalf("expected proposal to be enqueued unchanged")
		}
	default:
		t.Fatalf("expected proposal to be queued after height sync")
	}
}
