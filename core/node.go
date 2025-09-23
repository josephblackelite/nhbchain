package core

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"nhbchain/consensus/bft"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
	"nhbchain/native/loyalty"
	"nhbchain/p2p"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

// Node is the central controller, wiring all components together.
type Node struct {
	db             storage.Database
	state          *StateProcessor
	chain          *Blockchain
	validatorKey   *crypto.PrivateKey
	mempool        []*types.Transaction
	bftEngine      *bft.Engine
	stateMu        sync.Mutex
	escrowTreasury [20]byte
}

func NewNode(db storage.Database, key *crypto.PrivateKey, genesisPath string, allowAutogenesis bool) (*Node, error) {
	validatorAddr := key.PubKey().Address()
	fmt.Printf("Starting node with validator address: %s\n", validatorAddr.String())

	chain, err := NewBlockchain(db, genesisPath, allowAutogenesis)
	if err != nil {
		return nil, err
	}

	// Load current state root from the chain tip (if any), then open the trie.
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

	var treasury [20]byte
	copy(treasury[:], validatorAddr.Bytes())

	return &Node{
		db:             db,
		state:          stateProcessor,
		chain:          chain,
		validatorKey:   key,
		mempool:        make([]*types.Transaction, 0),
		escrowTreasury: treasury,
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
		proposal := new(bft.SignedProposal)
		if err := json.Unmarshal(msg.Payload, proposal); err != nil {
			return err
		}
		if n.bftEngine != nil {
			return n.bftEngine.HandleProposal(proposal)
		}

	case p2p.MsgTypeVote:
		vote := new(bft.SignedVote)
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
	n.mempool = []*types.Transaction{} // drain after read
	return txs
}

func (n *Node) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	header := &types.BlockHeader{
		Height:    n.chain.GetHeight() + 1,
		Timestamp: time.Now().Unix(),
		PrevHash:  n.chain.Tip(),
		Validator: n.validatorKey.PubKey().Address().Bytes(),
	}

	// Compute TxRoot over ordered transactions
	txRoot, err := ComputeTxRoot(txs)
	if err != nil {
		return nil, err
	}
	header.TxRoot = txRoot

	// Execute against a copy of StateDB to derive StateRoot
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
	// Remember parent root for rollback on any failure
	parentRoot := n.state.CurrentRoot()
	rollback := func() error {
		if err := n.state.ResetToRoot(parentRoot); err != nil {
			return fmt.Errorf("rollback to parent root: %w", err)
		}
		return nil
	}

	// Verify TxRoot before executing
	txRoot, err := ComputeTxRoot(b.Transactions)
	if err != nil {
		return err
	}
	if !bytes.Equal(txRoot, b.Header.TxRoot) {
		return fmt.Errorf("tx root mismatch")
	}

	// Apply transactions; abort (and rollback) on the first failure
	for i, tx := range b.Transactions {
		if err := n.state.ApplyTransaction(tx); err != nil {
			if rbErr := rollback(); rbErr != nil {
				return fmt.Errorf("apply transaction %d: %v (rollback failed: %w)", i, err, rbErr)
			}
			return fmt.Errorf("apply transaction %d: %w", i, err)
		}
	}

	// Check derived StateRoot matches header (if header set) or fill it
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

	// Commit state at this height
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

	// Persist block to the chain
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

func (n *Node) LoyaltyManager() *nhbstate.Manager {
	return nhbstate.NewManager(n.state.Trie)
}

func (n *Node) LoyaltyRegistry() *loyalty.Registry {
	return loyalty.NewRegistry(n.LoyaltyManager())
}

func (n *Node) LoyaltyBusinessByID(id loyalty.BusinessID) (*loyalty.Business, bool, error) {
	return n.state.LoyaltyBusinessByID(id)
}

func (n *Node) LoyaltyProgramByID(id loyalty.ProgramID) (*loyalty.Program, bool, error) {
	return n.state.LoyaltyProgramByID(id)
}

func (n *Node) LoyaltyProgramsByOwner(owner [20]byte) ([]loyalty.ProgramID, error) {
	return n.state.LoyaltyProgramsByOwner(owner)
}

var (
	// ErrEscrowNotFound is returned when an escrow record is missing from state.
	ErrEscrowNotFound = errors.New("escrow not found")
	// ErrTradeNotFound is returned when a trade record is missing from state.
	ErrTradeNotFound = errors.New("trade not found")
)

type escrowEventEmitter struct {
	node *Node
}

type eventWithPayload interface {
	Event() *types.Event
}

func (e escrowEventEmitter) Emit(evt events.Event) {
	if e.node == nil || evt == nil {
		return
	}
	payload, ok := evt.(eventWithPayload)
	if !ok {
		return
	}
	event := payload.Event()
	if event == nil {
		return
	}
	e.node.state.AppendEvent(event)
}

func (n *Node) newEscrowEngine(manager *nhbstate.Manager) *escrow.Engine {
	engine := escrow.NewEngine()
	engine.SetState(manager)
	engine.SetEmitter(escrowEventEmitter{node: n})
	engine.SetFeeTreasury(n.escrowTreasury)
	return engine
}

func (n *Node) newTradeEngine(manager *nhbstate.Manager) *escrow.TradeEngine {
	escrowEngine := n.newEscrowEngine(manager)
	tradeEngine := escrow.NewTradeEngine(escrowEngine)
	tradeEngine.SetState(manager)
	tradeEngine.SetEmitter(escrowEventEmitter{node: n})
	return tradeEngine
}

func (n *Node) EscrowCreate(payer, payee [20]byte, token string, amount *big.Int, feeBps uint32, deadline int64, mediator *[20]byte, meta [32]byte) ([32]byte, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	esc, err := engine.Create(payer, payee, token, amount, feeBps, deadline, mediator, meta)
	if err != nil {
		return [32]byte{}, err
	}
	return esc.ID, nil
}

func (n *Node) EscrowFund(id [32]byte, from [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Fund(id, from)
}

func (n *Node) EscrowRelease(id [32]byte, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Release(id, caller)
}

func (n *Node) EscrowRefund(id [32]byte, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Refund(id, caller)
}

func (n *Node) EscrowExpire(id [32]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Expire(id, time.Now().Unix())
}

func (n *Node) EscrowDispute(id [32]byte, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Dispute(id, caller)
}

func (n *Node) EscrowResolve(id [32]byte, caller [20]byte, outcome string) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Resolve(id, caller, outcome)
}

func (n *Node) EscrowGet(id [32]byte) (*escrow.Escrow, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	esc, ok := manager.EscrowGet(id)
	if !ok {
		return nil, ErrEscrowNotFound
	}
	return esc, nil
}

func (n *Node) EscrowVaultAddress(token string) ([20]byte, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	return manager.EscrowVaultAddress(token)
}

func (n *Node) P2PCreateTrade(offerID string, buyer, seller [20]byte,
	baseToken string, baseAmt *big.Int,
	quoteToken string, quoteAmt *big.Int,
	deadline int64) (tradeID [32]byte, escrowBaseID, escrowQuoteID [32]byte, err error) {

	trimmedOffer := strings.TrimSpace(offerID)
	if trimmedOffer == "" {
		return [32]byte{}, [32]byte{}, [32]byte{}, fmt.Errorf("trade: offerId is required")
	}
	normalizedBase, err := escrow.NormalizeToken(baseToken)
	if err != nil {
		return [32]byte{}, [32]byte{}, [32]byte{}, err
	}
	normalizedQuote, err := escrow.NormalizeToken(quoteToken)
	if err != nil {
		return [32]byte{}, [32]byte{}, [32]byte{}, err
	}
	if baseAmt == nil || baseAmt.Sign() <= 0 {
		return [32]byte{}, [32]byte{}, [32]byte{}, fmt.Errorf("trade: base amount must be positive")
	}
	if quoteAmt == nil || quoteAmt.Sign() <= 0 {
		return [32]byte{}, [32]byte{}, [32]byte{}, fmt.Errorf("trade: quote amount must be positive")
	}
	now := time.Now().Unix()
	if deadline < now {
		return [32]byte{}, [32]byte{}, [32]byte{}, fmt.Errorf("trade: deadline must be in the future")
	}

	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return [32]byte{}, [32]byte{}, [32]byte{}, fmt.Errorf("trade: failed to derive nonce: %w", err)
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	tradeEngine := n.newTradeEngine(manager)

	trade, err := tradeEngine.CreateTrade(trimmedOffer, buyer, seller, normalizedQuote, quoteAmt, normalizedBase, baseAmt, deadline, nonce)
	if err != nil {
		return [32]byte{}, [32]byte{}, [32]byte{}, err
	}
	return trade.ID, trade.EscrowBase, trade.EscrowQuote, nil
}

func (n *Node) P2PGetTrade(id [32]byte) (*escrow.Trade, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	trade, ok := manager.TradeGet(id)
	if !ok {
		return nil, ErrTradeNotFound
	}
	return trade.Clone(), nil
}

func (n *Node) P2PSettle(id [32]byte, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	trade, ok := manager.TradeGet(id)
	if !ok {
		return ErrTradeNotFound
	}
	if caller != trade.Buyer && caller != trade.Seller {
		return fmt.Errorf("trade: caller not participant")
	}
	tradeEngine := n.newTradeEngine(manager)
	return tradeEngine.SettleAtomic(id)
}

func (n *Node) P2PDispute(id [32]byte, caller [20]byte, msg string) error {
	_ = msg

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	trade, ok := manager.TradeGet(id)
	if !ok {
		return ErrTradeNotFound
	}
	if caller != trade.Buyer && caller != trade.Seller {
		return fmt.Errorf("trade: caller not participant")
	}
	tradeEngine := n.newTradeEngine(manager)
	return tradeEngine.TradeDispute(id, caller)
}

func (n *Node) P2PResolve(id [32]byte, arbitrator [20]byte, outcome string) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	if !n.state.HasRole("ROLE_ARBITRATOR", arbitrator[:]) {
		return fmt.Errorf("trade: caller lacks arbitrator role")
	}
	if _, ok := manager.TradeGet(id); !ok {
		return ErrTradeNotFound
	}
	tradeEngine := n.newTradeEngine(manager)
	return tradeEngine.TradeResolve(id, outcome)
}

func (n *Node) ResolveUsername(username string) ([]byte, bool) {
	return n.state.ResolveUsername(username)
}

func (n *Node) HasRole(role string, addr []byte) bool {
	return n.state.HasRole(role, addr)
}

// --- Both accessors are needed by different subsystems ---

// ChainID exposes the chain identifier (used by P2P authenticated handshake).
func (n *Node) ChainID() uint64 {
	return n.chain.ChainID()
}

// GetLastCommitHash returns a commit hash/seed (used by BFT proposer selection).
func (n *Node) GetLastCommitHash() []byte {
	return n.chain.Tip()
}

// Chain returns a reference to the node's blockchain object.
func (n *Node) Chain() *Blockchain {
	return n.chain
}
