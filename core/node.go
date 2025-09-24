package core

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"nhbchain/consensus/bft"
	"nhbchain/core/claimable"
	"nhbchain/core/engagement"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
	"nhbchain/native/loyalty"
	"nhbchain/p2p"
	"nhbchain/storage"
	"nhbchain/storage/trie"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
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
	engagementMgr  *engagement.Manager
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
		engagementMgr:  engagement.NewManager(stateProcessor.EngagementConfig()),
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

func (n *Node) StakeDelegate(delegator [20]byte, amount *big.Int, validator *[20]byte) (*types.Account, error) {
	if amount == nil {
		return nil, fmt.Errorf("amount required")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	var target []byte
	if validator != nil {
		// treat zero address as self-delegation
		zero := [20]byte{}
		if *validator != zero {
			target = validator[:]
		}
	}
	acct, err := n.state.StakeDelegate(delegator[:], target, amount)
	if err != nil {
		return nil, err
	}
	return acct, nil
}

func (n *Node) StakeUndelegate(delegator [20]byte, amount *big.Int) (*types.StakeUnbond, error) {
	if amount == nil {
		return nil, fmt.Errorf("amount required")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	return n.state.StakeUndelegate(delegator[:], amount)
}

func (n *Node) StakeClaim(delegator [20]byte, unbondID uint64) (*types.StakeUnbond, error) {
	if unbondID == 0 {
		return nil, fmt.Errorf("unbondingId must be greater than zero")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	return n.state.StakeClaim(delegator[:], unbondID)
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

func (n *Node) IdentitySetAlias(addr [20]byte, alias string) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	previous, _ := manager.IdentityReverse(addr[:])
	if err := manager.IdentitySetAlias(addr[:], alias); err != nil {
		return err
	}
	current, ok := manager.IdentityReverse(addr[:])
	if !ok || current == "" {
		return fmt.Errorf("identity: failed to persist alias")
	}
	if previous == current {
		if previous == "" {
			evt := events.IdentityAliasSet{Alias: current, Address: addr}.Event()
			if evt != nil {
				n.state.AppendEvent(evt)
			}
		}
		return nil
	}
	if previous == "" {
		evt := events.IdentityAliasSet{Alias: current, Address: addr}.Event()
		if evt != nil {
			n.state.AppendEvent(evt)
		}
	} else {
		evt := events.IdentityAliasRenamed{OldAlias: previous, NewAlias: current, Address: addr}.Event()
		if evt != nil {
			n.state.AppendEvent(evt)
		}
	}
	return nil
}

func (n *Node) IdentityResolve(alias string) ([20]byte, bool) {
	manager := nhbstate.NewManager(n.state.Trie)
	return manager.IdentityResolve(alias)
}

func (n *Node) IdentityReverse(addr [20]byte) (string, bool) {
	manager := nhbstate.NewManager(n.state.Trie)
	return manager.IdentityReverse(addr[:])
}

func (n *Node) EngagementRegisterDevice(addr [20]byte, deviceID string) (string, error) {
	if n.engagementMgr == nil {
		return "", fmt.Errorf("engagement manager unavailable")
	}
	validator := n.validatorKey.PubKey().Address()
	if !bytes.Equal(addr[:], validator.Bytes()) {
		return "", fmt.Errorf("device must register validator address %s", validator.String())
	}
	return n.engagementMgr.RegisterDevice(addr, deviceID)
}

func (n *Node) EngagementSubmitHeartbeat(deviceID, token string, timestamp int64) (int64, error) {
	if n.engagementMgr == nil {
		return 0, fmt.Errorf("engagement manager unavailable")
	}
	ts, err := n.engagementMgr.SubmitHeartbeat(deviceID, token, timestamp)
	if err != nil {
		return 0, err
	}

	validator := n.validatorKey.PubKey().Address()
	payload := types.HeartbeatPayload{DeviceID: deviceID, Timestamp: ts}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	account, err := n.state.GetAccount(validator.Bytes())
	if err != nil {
		return 0, err
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeHeartbeat,
		Nonce:    account.Nonce,
		Data:     data,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(n.validatorKey.PrivateKey); err != nil {
		return 0, err
	}
	n.mempool = append(n.mempool, tx)
	return ts, nil
}

func (n *Node) ClaimableCreate(payer [20]byte, token string, amount *big.Int, hashLock [32]byte, deadline int64) ([32]byte, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, err := manager.CreateClaimable(payer, token, amount, hashLock, deadline)
	if err != nil {
		return [32]byte{}, err
	}
	evt := events.ClaimableCreated{
		ID:        record.ID,
		Payer:     record.Payer,
		Token:     record.Token,
		Amount:    record.Amount,
		Deadline:  record.Deadline,
		CreatedAt: record.CreatedAt,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return record.ID, nil
}

func (n *Node) ClaimableClaim(id [32]byte, preimage []byte, payee [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, changed, err := manager.ClaimableClaim(id, preimage, payee)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	evt := events.ClaimableClaimed{
		ID:     record.ID,
		Payer:  record.Payer,
		Payee:  payee,
		Token:  record.Token,
		Amount: record.Amount,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return nil
}

func (n *Node) ClaimableCancel(id [32]byte, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, changed, err := manager.ClaimableCancel(id, caller, time.Now().Unix())
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	evt := events.ClaimableCancelled{
		ID:     record.ID,
		Payer:  record.Payer,
		Token:  record.Token,
		Amount: record.Amount,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return nil
}

func (n *Node) ClaimableExpire(id [32]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, changed, err := manager.ClaimableExpire(id, time.Now().Unix())
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	evt := events.ClaimableExpired{
		ID:     record.ID,
		Payer:  record.Payer,
		Token:  record.Token,
		Amount: record.Amount,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return nil
}

func (n *Node) ClaimableGet(id [32]byte) (*claimable.Claimable, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, ok := manager.ClaimableGet(id)
	if !ok {
		return nil, claimable.ErrNotFound
	}
	return record, nil
}

// MintWithSignature credits funds to the recipient described in the voucher after
// verifying the off-chain signature and replay protection fields.
func (n *Node) MintWithSignature(voucher *MintVoucher, signature []byte) (string, error) {
	if voucher == nil {
		return "", fmt.Errorf("voucher required")
	}
	amount, err := voucher.AmountBig()
	if err != nil {
		return "", err
	}
	if voucher.ChainID != MintChainID {
		return "", ErrMintInvalidChainID
	}
	if voucher.Expiry <= time.Now().Unix() {
		return "", ErrMintExpired
	}
	canonical, err := voucher.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := ethcrypto.Keccak256(canonical)
	if len(signature) != 65 {
		return "", fmt.Errorf("invalid signature length")
	}
	pubKey, err := ethcrypto.SigToPub(digest, signature)
	if err != nil {
		return "", fmt.Errorf("recover signer: %w", err)
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	var recoveredBytes [20]byte
	copy(recoveredBytes[:], recovered.Bytes())

	token := voucher.NormalizedToken()
	var requiredRole string
	switch token {
	case "NHB":
		requiredRole = "MINTER_NHB"
	case "ZNHB":
		requiredRole = "MINTER_ZNHB"
	default:
		return "", fmt.Errorf("unsupported token %q", voucher.Token)
	}

	invoiceID := voucher.TrimmedInvoiceID()
	if invoiceID == "" {
		return "", fmt.Errorf("invoiceId required")
	}
	recipientRef := voucher.TrimmedRecipient()
	if recipientRef == "" {
		return "", fmt.Errorf("recipient required")
	}

	txHashBytes := ethcrypto.Keccak256(append([]byte{}, append(canonical, signature...)...))
	txHash := "0x" + strings.ToLower(hex.EncodeToString(txHashBytes))

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)

	if !manager.HasRole(requiredRole, recoveredBytes[:]) {
		return "", ErrMintInvalidSigner
	}

	key := nhbstate.MintInvoiceKey(invoiceID)
	var used bool
	if ok, err := manager.KVGet(key, &used); err != nil {
		return "", err
	} else if ok && used {
		return "", ErrMintInvoiceUsed
	}

	var recipient [20]byte
	if decoded, err := crypto.DecodeAddress(recipientRef); err == nil {
		copy(recipient[:], decoded.Bytes())
	} else {
		resolved, ok := manager.IdentityResolve(recipientRef)
		if !ok {
			return "", fmt.Errorf("recipient not found: %s", recipientRef)
		}
		recipient = resolved
	}

	account, err := manager.GetAccount(recipient[:])
	if err != nil {
		return "", err
	}
	switch token {
	case "NHB":
		account.BalanceNHB = new(big.Int).Add(account.BalanceNHB, amount)
	case "ZNHB":
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)
	}
	if err := manager.PutAccount(recipient[:], account); err != nil {
		return "", err
	}
	if err := manager.KVPut(key, true); err != nil {
		return "", err
	}

	evt := events.MintSettled{
		InvoiceID: invoiceID,
		Recipient: recipient,
		Token:     token,
		Amount:    amount,
		TxHash:    txHash,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}

	return txHash, nil
}

func (n *Node) ResolveUsername(username string) ([]byte, bool) {
	return n.state.ResolveUsername(username)
}

func (n *Node) HasRole(role string, addr []byte) bool {
	return n.state.HasRole(role, addr)
}

// --- Both accessors are needed by different subsystems ---

// GenesisHash exposes the canonical genesis hash for the local chain.
func (n *Node) GenesisHash() []byte {
	return n.chain.GenesisHash()
}

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
