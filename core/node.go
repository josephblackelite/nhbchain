package core

import (
	"encoding/json"
	"fmt"
	"math/big"
	"nhbchain/consensus/bft"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"
	"nhbchain/storage"
	"nhbchain/storage/trie"
	"time"
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
	stateTrie := trie.NewTrie(db, nil)
	chain, err := NewBlockchain(db)
	if err != nil {
		return nil, err
	}
	stateProcessor := NewStateProcessor(stateTrie)

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

func (n *Node) CreateBlock(txs []*types.Transaction) *types.Block {
	header := &types.BlockHeader{
		Height:    n.chain.GetHeight() + 1,
		Timestamp: time.Now().Unix(),
		PrevHash:  n.chain.Tip(),
		StateRoot: n.state.Trie.Root,
		Validator: n.validatorKey.PubKey().Address().Bytes(),
	}
	return types.NewBlock(header, txs)
}

func (n *Node) CommitBlock(b *types.Block) {
	for _, tx := range b.Transactions {
		n.state.ApplyTransaction(tx)
	}
	n.chain.AddBlock(b)
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