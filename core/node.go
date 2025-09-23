package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"nhbchain/consensus/bft"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

// Node is the central controller, wiring all components together.
type Node struct {
	db           storage.Database
	state        *StateProcessor
	chain        *Blockchain
	validatorKey *crypto.PrivateKey
	mempool      []*types.Transaction
	bftEngine    *bft.Engine
}

func NewNode(db storage.Database, key *crypto.PrivateKey) (*Node, error) {
	validatorAddr := key.PubKey().Address()
	fmt.Printf("Starting node with validator address: %s\n", validatorAddr.String())
	chain, err := NewBlockchain(db)
	if err != nil {
		return nil, err
	}
	var root []byte
	if header := chain.CurrentHeader(); header != nil {
		root = header.StateRoot
	}
	stateTrie, err := trie.NewTrie(db, root)
	if err != nil {
		return nil, err
	}
	stateProcessor, err := NewStateProcessor(stateTrie)
	if err != nil {
		return nil, err
	}

	return &Node{
		db:           db,
		state:        stateProcessor,
		chain:        chain,
		validatorKey: key,
		mempool:      make([]*types.Transaction, 0),
	}, nil
}

func (n *Node) SetBftEngine(bftEngine *bft.Engine) {
	n.bftEngine = bftEngine
}

func (n *Node) StartConsensus() {
	if n.bftEngine != nil {
		n.bftEngine.Start()
	}
}

// HandleMessage is the central router for all incoming P2P messages.
// It satisfies the p2p.MessageHandler interface.
func (n *Node) HandleMessage(msg *p2p.Message) error {
	switch msg.Type {
	case p2p.MsgTypeTx:
		tx := new(types.Transaction)
		if err := json.Unmarshal(msg.Payload, tx); err != nil {
			return err
		}
		n.AddTransaction(tx)
	case p2p.MsgTypeProposal:
		proposal := new(bft.Proposal)
		if err := json.Unmarshal(msg.Payload, proposal); err != nil {
			return err
		}
		if n.bftEngine != nil {
			return n.bftEngine.HandleProposal(proposal)
		}
	case p2p.MsgTypeVote:
		vote := new(bft.Vote)
		if err := json.Unmarshal(msg.Payload, vote); err != nil {
			return err
		}
		if n.bftEngine != nil {
			return n.bftEngine.HandleVote(vote)
		}
	}
	return nil
}

func (n *Node) AddTransaction(tx *types.Transaction) {
	n.mempool = append(n.mempool, tx)
}

// --- Methods for bft.NodeInterface ---

func (n *Node) GetMempool() []*types.Transaction {
	txs := make([]*types.Transaction, len(n.mempool))
	copy(txs, n.mempool)
	n.mempool = []*types.Transaction{}
	return txs
}

func (n *Node) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	header := &types.BlockHeader{
		Height:    n.chain.GetHeight() + 1,
		Timestamp: time.Now().Unix(),
		PrevHash:  n.chain.Tip(),
		Validator: n.validatorKey.PubKey().Address().Bytes(),
	}

	txRoot, err := ComputeTxRoot(txs)
	if err != nil {
		return nil, err
	}
	header.TxRoot = txRoot

	stateCopy, err := n.state.Copy()
	if err != nil {
		return nil, err
	}
	for _, tx := range txs {
		if err := stateCopy.ApplyTransaction(tx); err != nil {
			return nil, err
		}
	}
	stateRoot := stateCopy.PendingRoot()
	header.StateRoot = stateRoot.Bytes()

	return types.NewBlock(header, txs), nil
}

func (n *Node) CommitBlock(b *types.Block) error {
	parentRoot := n.state.CurrentRoot()

	rollback := func() error {
		if err := n.state.ResetToRoot(parentRoot); err != nil {
			return fmt.Errorf("rollback to parent root: %w", err)
		}
		return nil
	}

	txRoot, err := ComputeTxRoot(b.Transactions)
	if err != nil {
		return err
	}
	if !bytes.Equal(txRoot, b.Header.TxRoot) {
		return fmt.Errorf("tx root mismatch")
	}

	for i, tx := range b.Transactions {
		if err := n.state.ApplyTransaction(tx); err != nil {
			if rbErr := rollback(); rbErr != nil {
				return fmt.Errorf("apply transaction %d: %v (rollback failed: %w)", i, err, rbErr)
			}
			return fmt.Errorf("apply transaction %d: %w", i, err)
		}
	}

	pendingRoot := n.state.PendingRoot()
	pendingBytes := pendingRoot.Bytes()
	if len(b.Header.StateRoot) == 0 {
		b.Header.StateRoot = pendingBytes
	} else if !bytes.Equal(b.Header.StateRoot, pendingBytes) {
		if rbErr := rollback(); rbErr != nil {
			return fmt.Errorf("state root mismatch: %w", rbErr)
		}
		return fmt.Errorf("state root mismatch")
	}

	committedRoot, err := n.state.Commit(b.Header.Height)
	if err != nil {
		if rbErr := rollback(); rbErr != nil {
			return fmt.Errorf("state commit failed: %v (rollback failed: %w)", err, rbErr)
		}
		return fmt.Errorf("state commit failed: %w", err)
	}
	committedBytes := committedRoot.Bytes()
	if !bytes.Equal(b.Header.StateRoot, committedBytes) {
		return fmt.Errorf("state root mismatch after commit")
	}

	if err := n.chain.AddBlock(b); err != nil {
		return err
	}
	return nil
}

func (n *Node) GetValidatorSet() map[string]*big.Int { return n.state.ValidatorSet }
func (n *Node) GetHeight() uint64                    { return n.chain.GetHeight() }
func (n *Node) GetAccount(addr []byte) (*types.Account, error) {
	return n.state.GetAccount(addr)
}

// Chain returns a reference to the node's blockchain object.
func (n *Node) Chain() *Blockchain { // ✅ same package, no prefix
	return n.chain
}
