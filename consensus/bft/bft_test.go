package bft

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"
)

type failingNode struct {
	validatorSet map[string]*big.Int
	commitErr    error
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

type recordingBroadcaster struct {
	messages []*p2p.Message
}

func (r *recordingBroadcaster) Broadcast(msg *p2p.Message) error {
	r.messages = append(r.messages, msg)
	return nil
}

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
	var vote Vote
	if err := json.Unmarshal(msg.Payload, &vote); err != nil {
		t.Fatalf("unmarshal vote: %v", err)
	}
	if vote.Type != Prevote {
		t.Fatalf("expected prevote type, got %v", vote.Type)
	}
	if len(vote.BlockHash) != 0 {
		t.Fatalf("expected nil block hash in prevote, got %x", vote.BlockHash)
	}
	if _, ok := engine.receivedVotes[Prevote][string(validatorAddr)]; !ok {
		t.Fatalf("expected prevote record for validator")
	}
	if engine.committedBlocks[engine.currentState.Height] {
		t.Fatalf("block should not be marked committed on execution failure")
	}
}
