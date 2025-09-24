package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"nhbchain/core/engagement"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
	"nhbchain/native/loyalty"
	"nhbchain/storage/trie"

	"github.com/ethereum/go-ethereum/common"
	gethcore "github.com/ethereum/go-ethereum/core"
	gethstate "github.com/ethereum/go-ethereum/core/state"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	gethvm "github.com/ethereum/go-ethereum/core/vm"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

const MINIMUM_STAKE = 1000
const engagementDayFormat = "2006-01-02"

// Privileged arbitrator address (replace with multisig in production).
var ARBITRATOR_ADDRESS = common.HexToAddress("0x00000000000000000000000000000000000000AA")

var (
	accountMetadataPrefix = []byte("account-meta:")
	usernameIndexKey      = ethcrypto.Keccak256([]byte("username-index"))
	validatorSetKey       = ethcrypto.Keccak256([]byte("validator-set"))
)

type StateProcessor struct {
	Trie             *trie.Trie
	stateDB          *gethstate.CachingDB
	LoyaltyEngine    *loyalty.Engine
	EscrowEngine     *escrow.Engine
	TradeEngine      *escrow.TradeEngine
	usernameToAddr   map[string][]byte
	ValidatorSet     map[string]*big.Int
	committedRoot    common.Hash
	events           []types.Event
	engagementConfig engagement.Config
}

func NewStateProcessor(tr *trie.Trie) (*StateProcessor, error) {
	stateDB := gethstate.NewDatabase(tr.TrieDB(), nil)
	escEngine := escrow.NewEngine()
	tradeEngine := escrow.NewTradeEngine(escEngine)
	sp := &StateProcessor{
		Trie:             tr,
		stateDB:          stateDB,
		LoyaltyEngine:    loyalty.NewEngine(),
		EscrowEngine:     escEngine,
		TradeEngine:      tradeEngine,
		usernameToAddr:   make(map[string][]byte),
		ValidatorSet:     make(map[string]*big.Int),
		committedRoot:    tr.Root(),
		events:           make([]types.Event, 0),
		engagementConfig: engagement.DefaultConfig(),
	}
	if err := sp.loadUsernameIndex(); err != nil {
		return nil, err
	}
	if err := sp.loadValidatorSet(); err != nil {
		return nil, err
	}
	return sp, nil
}

// EngagementConfig returns the configuration currently used for engagement
// scoring.
func (sp *StateProcessor) EngagementConfig() engagement.Config {
	return sp.engagementConfig
}

// SetEngagementConfig replaces the engagement configuration. Callers must
// ensure the new configuration is valid network wide.
func (sp *StateProcessor) SetEngagementConfig(cfg engagement.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	sp.engagementConfig = cfg
	return nil
}

// CurrentRoot returns the last committed state root.
func (sp *StateProcessor) CurrentRoot() common.Hash {
	return sp.committedRoot
}

// PendingRoot returns the root of the trie including in-memory mutations.
func (sp *StateProcessor) PendingRoot() common.Hash {
	return sp.Trie.Hash()
}

// ResetToRoot discards any in-memory changes and reloads the trie at the
// provided root hash.
func (sp *StateProcessor) ResetToRoot(root common.Hash) error {
	if err := sp.Trie.Reset(root); err != nil {
		return err
	}
	sp.committedRoot = root
	return nil
}

// Commit persists the current trie contents and returns the resulting state
// root.
func (sp *StateProcessor) Commit(blockNumber uint64) (common.Hash, error) {
	newRoot, err := sp.Trie.Commit(sp.committedRoot, blockNumber)
	if err != nil {
		return common.Hash{}, err
	}
	sp.committedRoot = newRoot
	return newRoot, nil
}

// Copy returns a shallow clone of the state processor that can be used for
// speculative execution without mutating the canonical state.
func (sp *StateProcessor) Copy() (*StateProcessor, error) {
	trieCopy, err := sp.Trie.Copy()
	if err != nil {
		return nil, err
	}
	usernameCopy := make(map[string][]byte, len(sp.usernameToAddr))
	for k, v := range sp.usernameToAddr {
		usernameCopy[k] = append([]byte(nil), v...)
	}
	validatorCopy := make(map[string]*big.Int, len(sp.ValidatorSet))
	for k, v := range sp.ValidatorSet {
		validatorCopy[k] = new(big.Int).Set(v)
	}
	eventsCopy := make([]types.Event, len(sp.events))
	for i := range sp.events {
		attrs := make(map[string]string, len(sp.events[i].Attributes))
		for k, v := range sp.events[i].Attributes {
			attrs[k] = v
		}
		eventsCopy[i] = types.Event{Type: sp.events[i].Type, Attributes: attrs}
	}
	return &StateProcessor{
		Trie:             trieCopy,
		stateDB:          sp.stateDB,
		LoyaltyEngine:    sp.LoyaltyEngine,
		EscrowEngine:     sp.EscrowEngine,
		TradeEngine:      sp.TradeEngine,
		usernameToAddr:   usernameCopy,
		ValidatorSet:     validatorCopy,
		committedRoot:    sp.committedRoot,
		events:           eventsCopy,
		engagementConfig: sp.engagementConfig,
	}, nil
}

func (sp *StateProcessor) ApplyTransaction(tx *types.Transaction) error {
	if tx.Type == types.TxTypeTransfer {
		return sp.applyEvmTransaction(tx)
	}
	return sp.handleNativeTransaction(tx)
}

// --- EVM path (Geth v1.16.x) ---
func (sp *StateProcessor) applyEvmTransaction(tx *types.Transaction) error {
	from, err := tx.From()
	if err != nil {
		return err
	}
	parentRoot := sp.Trie.Hash()
	statedb, err := gethstate.New(parentRoot, sp.stateDB)
	if err != nil {
		return fmt.Errorf("statedb init: %w", err)
	}

	fromAddr := common.BytesToAddress(from)
	var toAddrPtr *common.Address
	if tx.To != nil {
		addr := common.BytesToAddress(tx.To)
		toAddrPtr = &addr
	}

	// Build contexts + message (struct literal in v1.16)
	blockCtx := gethvm.BlockContext{
		Coinbase:    common.Address{},
		BlockNumber: big.NewInt(0),
		Time:        uint64(time.Now().Unix()),
		Difficulty:  big.NewInt(0),
	}

	msg := gethcore.Message{
		From:          fromAddr,
		To:            toAddrPtr,
		Nonce:         tx.Nonce,
		Value:         tx.Value,
		GasLimit:      tx.GasLimit,
		GasPrice:      tx.GasPrice,
		GasFeeCap:     tx.GasPrice, // simple: reuse
		GasTipCap:     tx.GasPrice, // simple: reuse
		Data:          tx.Data,
		AccessList:    nil,
		BlobGasFeeCap: nil,
		BlobHashes:    nil,
		// NOTE: v1.16 has no SkipAccountChecks; do not set
	}
	txCtx := gethcore.NewEVMTxContext(&msg) // pointer expected

	// 3) NewEVM signature for v1.16, then set tx context
	evm := gethvm.NewEVM(blockCtx, statedb, params.TestChainConfig, gethvm.Config{
		NoBaseFee: true, // disable basefee in this environment
	})
	evm.SetTxContext(txCtx)

	// 4) Execute
	gp := new(gethcore.GasPool).AddGas(tx.GasLimit)
	result, err := gethcore.ApplyMessage(evm, &msg, gp) // pointer expected
	if err != nil {
		return fmt.Errorf("ApplyMessage: %w", err)
	}
	if result != nil && result.Err != nil {
		return fmt.Errorf("EVM error: %w", result.Err)
	}

	newRoot, err := statedb.Commit(0, false, false)
	if err != nil {
		return fmt.Errorf("statedb commit: %w", err)
	}
	if err := sp.Trie.Reset(newRoot); err != nil {
		return fmt.Errorf("trie reset: %w", err)
	}

	fromAcc, err := sp.getAccount(from)
	if err != nil {
		return err
	}
	var toAcc *types.Account
	if tx.To != nil {
		toAcc, err = sp.getAccount(tx.To)
		if err != nil {
			return err
		}
	}

	if tx.To != nil {
		ctx := &loyalty.BaseRewardContext{
			From:  append([]byte(nil), from...),
			To:    append([]byte(nil), tx.To...),
			Token: "NHB",
			Amount: func() *big.Int {
				if tx.Value == nil {
					return big.NewInt(0)
				}
				return new(big.Int).Set(tx.Value)
			}(),
			Timestamp:   time.Unix(int64(blockCtx.Time), 0),
			FromAccount: fromAcc,
			ToAccount:   toAcc,
		}
		sp.LoyaltyEngine.OnTransactionSuccess(sp, ctx)
	}

	if err := sp.setAccount(from, fromAcc); err != nil {
		return err
	}
	if tx.To != nil && toAcc != nil {
		if err := sp.setAccount(tx.To, toAcc); err != nil {
			return err
		}
	}

	if err := sp.recordEngagementActivity(from, time.Now().UTC(), 1, 0, 0); err != nil {
		return err
	}

	fmt.Printf("EVM transaction processed. Gas used: %d. Output: %x\n", result.UsedGas, result.ReturnData)
	return nil
}

// --- Native handlers (original semantics + new dispute flow) ---

func (sp *StateProcessor) handleNativeTransaction(tx *types.Transaction) error {
	sender, err := tx.From()
	if err != nil {
		return err
	}
	switch tx.Type {
	case types.TxTypeRegisterIdentity:
		if err := sp.applyRegisterIdentity(tx); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, time.Now().UTC(), 1, 0, 0)
	case types.TxTypeCreateEscrow:
		if err := sp.applyCreateEscrow(tx); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, time.Now().UTC(), 1, 1, 0)
	case types.TxTypeReleaseEscrow:
		if err := sp.applyReleaseEscrow(tx); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, time.Now().UTC(), 1, 1, 0)
	case types.TxTypeRefundEscrow:
		if err := sp.applyRefundEscrow(tx); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, time.Now().UTC(), 1, 1, 0)
	case types.TxTypeStake:
		if err := sp.applyStake(tx); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, time.Now().UTC(), 1, 0, 1)
	case types.TxTypeUnstake:
		if err := sp.applyUnstake(tx); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, time.Now().UTC(), 1, 0, 1)
	case types.TxTypeHeartbeat:
		return sp.applyHeartbeat(tx)

	// --- NEW DISPUTE RESOLUTION CASES ---
	case types.TxTypeLockEscrow:
		if err := sp.applyLockEscrow(tx); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, time.Now().UTC(), 1, 1, 0)
	case types.TxTypeDisputeEscrow:
		if err := sp.applyDisputeEscrow(tx); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, time.Now().UTC(), 1, 1, 0)
	case types.TxTypeArbitrateRelease:
		if err := sp.applyArbitrate(tx, true); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, time.Now().UTC(), 1, 1, 0)
	case types.TxTypeArbitrateRefund:
		if err := sp.applyArbitrate(tx, false); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, time.Now().UTC(), 1, 1, 0)
	}
	return fmt.Errorf("unknown native transaction type: %d", tx.Type)
}

func (sp *StateProcessor) applyRegisterIdentity(tx *types.Transaction) error {
	from, _ := tx.From()
	username := string(tx.Data)
	if len(username) < 3 || len(username) > 20 {
		return fmt.Errorf("username must be 3-20 characters")
	}
	if _, ok := sp.usernameToAddr[username]; ok {
		return fmt.Errorf("username '%s' taken", username)
	}
	fromAccount, _ := sp.getAccount(from)
	if fromAccount.Username != "" {
		return fmt.Errorf("account already has username")
	}
	fromAccount.Username = username
	fromAccount.Nonce++
	sp.setAccount(from, fromAccount)
	fmt.Printf("Identity processed: Username '%s' registered to %s.\n",
		username, crypto.NewAddress(crypto.NHBPrefix, from).String())
	return nil
}

func (sp *StateProcessor) applyCreateEscrow(tx *types.Transaction) error {
	from, _ := tx.From() // This is the person creating the escrow (always the seller with the asset)

	// The data payload can now optionally contain a pre-defined buyer
	var escrowData struct {
		Seller []byte   `json:"seller"`
		Amount *big.Int `json:"amount"`
		Buyer  []byte   `json:"buyer,omitempty"` // Optional: The buyer accepting a "Want to Buy" offer
	}
	if err := json.Unmarshal(tx.Data, &escrowData); err != nil {
		return fmt.Errorf("invalid escrow data: %w", err)
	}
	if escrowData.Amount == nil || escrowData.Amount.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("escrow amount must be positive")
	}

	sellerAccount, _ := sp.getAccount(from)
	if sellerAccount.BalanceNHB.Cmp(escrowData.Amount) < 0 {
		return fmt.Errorf("insufficient funds to create escrow")
	}

	// Debit the seller's account and increment their nonce
	sellerAccount.BalanceNHB.Sub(sellerAccount.BalanceNHB, escrowData.Amount)
	sellerAccount.Nonce++

	escrowID, _ := tx.Hash()
	newEscrow := escrow.LegacyEscrow{
		ID:     escrowID,
		Seller: from, // The creator of the tx is always the seller with the asset
		Amount: escrowData.Amount,
	}

	// --- THE SYMMETRICAL ESCROW UPGRADE ---
	if escrowData.Buyer != nil {
		// This is a "Buy Offer" being accepted. The escrow starts locked.
		newEscrow.Buyer = escrowData.Buyer
		newEscrow.Status = escrow.LegacyStatusInProgress
		fmt.Printf("Symmetrical Escrow Created (In Progress): Seller %s locks funds for Buyer %s, Amount: %s, ID: %x\n",
			crypto.NewAddress(crypto.NHBPrefix, from).String(),
			crypto.NewAddress(crypto.NHBPrefix, escrowData.Buyer).String(),
			newEscrow.Amount.String(), newEscrow.ID)
	} else {
		// This is a standard "Sell Offer". The escrow starts open, and the seller is the initial "buyer".
		newEscrow.Buyer = from
		newEscrow.Status = escrow.LegacyStatusOpen
		fmt.Printf("Standard Escrow Created (Open): Seller %s lists %s NHBCoin, ID: %x\n",
			crypto.NewAddress(crypto.NHBPrefix, from).String(),
			newEscrow.Amount.String(), newEscrow.ID)
	}

	// Save the final state to the trie
	if err := sp.setAccount(from, sellerAccount); err != nil {
		return err
	}
	if err := sp.setEscrow(escrowID, &newEscrow); err != nil {
		return err
	}

	return nil
}

func (sp *StateProcessor) applyReleaseEscrow(tx *types.Transaction) error {
	sender, _ := tx.From()
	escrowID := tx.Data
	e, err := sp.getEscrow(escrowID)
	if err != nil {
		return err
	}
	if e.Status != escrow.LegacyStatusOpen {
		return fmt.Errorf("escrow not open")
	}
	if string(sender) != string(e.Buyer) {
		return fmt.Errorf("only buyer can release")
	}
	e.Status = escrow.LegacyStatusReleased
	sellerAccount, _ := sp.getAccount(e.Seller)
	senderAccount, _ := sp.getAccount(sender)
	sellerAccount.BalanceNHB.Add(sellerAccount.BalanceNHB, e.Amount)
	senderAccount.Nonce++
	sp.setAccount(e.Seller, sellerAccount)
	sp.setEscrow(escrowID, e)
	sp.setAccount(sender, senderAccount)
	fmt.Printf("Escrow released: Funds (%s NHB) to seller %s.\n",
		e.Amount.String(), crypto.NewAddress(crypto.NHBPrefix, e.Seller).String())
	return nil
}

func (sp *StateProcessor) applyRefundEscrow(tx *types.Transaction) error {
	sender, _ := tx.From()
	escrowID := tx.Data
	e, err := sp.getEscrow(escrowID)
	if err != nil {
		return err
	}
	if e.Status != escrow.LegacyStatusOpen {
		return fmt.Errorf("escrow not open")
	}
	if string(sender) != string(e.Seller) {
		return fmt.Errorf("only seller can refund")
	}
	e.Status = escrow.LegacyStatusRefunded
	buyerAccount, _ := sp.getAccount(e.Buyer)
	senderAccount, _ := sp.getAccount(sender)
	buyerAccount.BalanceNHB.Add(buyerAccount.BalanceNHB, e.Amount)
	senderAccount.Nonce++
	sp.setAccount(e.Buyer, buyerAccount)
	sp.setEscrow(escrowID, e)
	sp.setAccount(sender, senderAccount)
	fmt.Printf("Escrow refunded: Funds (%s NHB) to buyer %s.\n",
		e.Amount.String(), crypto.NewAddress(crypto.NHBPrefix, e.Buyer).String())
	return nil
}

// --- NEW: Lock -> Dispute -> Arbitrate flow ---

func (sp *StateProcessor) applyLockEscrow(tx *types.Transaction) error {
	sender, _ := tx.From() // prospective buyer engaging the escrow
	escrowID := tx.Data
	e, err := sp.getEscrow(escrowID)
	if err != nil {
		return err
	}

	if e.Status != escrow.LegacyStatusOpen {
		return fmt.Errorf("escrow is not open to be locked")
	}

	e.Buyer = sender
	e.Status = escrow.LegacyStatusInProgress

	senderAccount, _ := sp.getAccount(sender)
	senderAccount.Nonce++

	if err := sp.setEscrow(escrowID, e); err != nil {
		return err
	}
	if err := sp.setAccount(sender, senderAccount); err != nil {
		return err
	}

	fmt.Printf("Escrow Locked: Escrow %x is now in progress for buyer %s.\n",
		escrowID, crypto.NewAddress(crypto.NHBPrefix, sender).String())
	return nil
}

func (sp *StateProcessor) applyDisputeEscrow(tx *types.Transaction) error {
	sender, _ := tx.From()
	escrowID := tx.Data
	e, err := sp.getEscrow(escrowID)
	if err != nil {
		return err
	}

	if e.Status != escrow.LegacyStatusInProgress {
		return fmt.Errorf("only an in-progress escrow can be disputed")
	}
	if !bytes.Equal(sender, e.Buyer) {
		return fmt.Errorf("only the buyer can dispute an escrow")
	}

	e.Status = escrow.LegacyStatusDisputed

	senderAccount, _ := sp.getAccount(sender)
	senderAccount.Nonce++

	if err := sp.setEscrow(escrowID, e); err != nil {
		return err
	}
	if err := sp.setAccount(sender, senderAccount); err != nil {
		return err
	}

	fmt.Printf("Escrow Disputed: Escrow %x has been flagged for arbitration.\n", escrowID)
	return nil
}

func (sp *StateProcessor) applyArbitrate(tx *types.Transaction, releaseToBuyer bool) error {
	sender, _ := tx.From()

	// Only the privileged arbitrator can execute
	if !bytes.Equal(sender, ARBITRATOR_ADDRESS.Bytes()) {
		return fmt.Errorf("sender is not the authorized arbitrator")
	}

	escrowID := tx.Data
	e, err := sp.getEscrow(escrowID)
	if err != nil {
		return err
	}

	if e.Status != escrow.LegacyStatusDisputed {
		return fmt.Errorf("escrow is not in a disputed state")
	}

	if releaseToBuyer {
		e.Status = escrow.LegacyStatusReleased
		buyerAccount, _ := sp.getAccount(e.Buyer)
		buyerAccount.BalanceNHB.Add(buyerAccount.BalanceNHB, e.Amount)
		if err := sp.setAccount(e.Buyer, buyerAccount); err != nil {
			return err
		}
		fmt.Printf("Arbitration complete: Escrow %x released to buyer.\n", escrowID)
	} else {
		e.Status = escrow.LegacyStatusRefunded
		sellerAccount, _ := sp.getAccount(e.Seller)
		sellerAccount.BalanceNHB.Add(sellerAccount.BalanceNHB, e.Amount)
		if err := sp.setAccount(e.Seller, sellerAccount); err != nil {
			return err
		}
		fmt.Printf("Arbitration complete: Escrow %x refunded to seller.\n", escrowID)
	}

	// Save final escrow state
	if err := sp.setEscrow(escrowID, e); err != nil {
		return err
	}
	return nil
}

func (sp *StateProcessor) applyStake(tx *types.Transaction) error {
	sender, _ := tx.From()
	stakeAmount := tx.Value
	if stakeAmount == nil || stakeAmount.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("stake must be positive")
	}
	wasValidator := sp.IsValidator(sender)
	senderAccount, _ := sp.getAccount(sender)
	if senderAccount.BalanceZNHB.Cmp(stakeAmount) < 0 {
		return fmt.Errorf("insufficient ZapNHB")
	}
	senderAccount.BalanceZNHB.Sub(senderAccount.BalanceZNHB, stakeAmount)
	senderAccount.Stake.Add(senderAccount.Stake, stakeAmount)
	senderAccount.Nonce++
	sp.setAccount(sender, senderAccount)
	senderAddr := crypto.NewAddress(crypto.NHBPrefix, sender)
	if !wasValidator && senderAccount.Stake.Cmp(big.NewInt(MINIMUM_STAKE)) >= 0 {
		fmt.Printf("Account %s is now an active validator.\n", senderAddr.String())
	}
	fmt.Printf("Stake processed: Account %s staked %s ZapNHB.\n",
		senderAddr.String(), stakeAmount.String())
	return nil
}

func (sp *StateProcessor) applyUnstake(tx *types.Transaction) error {
	sender, _ := tx.From()
	unStakeAmount := tx.Value
	if unStakeAmount == nil || unStakeAmount.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("unstake must be positive")
	}
	wasValidator := sp.IsValidator(sender)
	senderAccount, _ := sp.getAccount(sender)
	if senderAccount.Stake.Cmp(unStakeAmount) < 0 {
		return fmt.Errorf("insufficient staked balance")
	}
	senderAccount.Stake.Sub(senderAccount.Stake, unStakeAmount)
	senderAccount.BalanceZNHB.Add(senderAccount.BalanceZNHB, unStakeAmount)
	senderAccount.Nonce++
	sp.setAccount(sender, senderAccount)
	senderAddr := crypto.NewAddress(crypto.NHBPrefix, sender)
	if wasValidator && senderAccount.Stake.Cmp(big.NewInt(MINIMUM_STAKE)) < 0 {
		fmt.Printf("Account %s is no longer an active validator.\n", senderAddr.String())
	}
	fmt.Printf("Un-stake processed: Account %s un-staked %s ZapNHB.\n",
		senderAddr.String(), unStakeAmount.String())
	return nil
}

func (sp *StateProcessor) applyHeartbeat(tx *types.Transaction) error {
	sender, err := tx.From()
	if err != nil {
		return err
	}
	payload := types.HeartbeatPayload{}
	if len(tx.Data) > 0 {
		if err := json.Unmarshal(tx.Data, &payload); err != nil {
			return fmt.Errorf("invalid heartbeat payload: %w", err)
		}
	}
	if payload.Timestamp == 0 {
		payload.Timestamp = time.Now().UTC().Unix()
	}
	now := time.Unix(payload.Timestamp, 0).UTC()

	senderAccount, err := sp.getAccount(sender)
	if err != nil {
		return err
	}

	updates := sp.rolloverEngagement(senderAccount, now)
	if senderAccount.EngagementLastHeartbeat != 0 {
		minDelta := int64(sp.engagementConfig.HeartbeatInterval.Seconds())
		last := int64(senderAccount.EngagementLastHeartbeat)
		if payload.Timestamp <= last {
			return fmt.Errorf("heartbeat replay detected")
		}
		if payload.Timestamp-last < minDelta {
			return fmt.Errorf("heartbeat rate limited")
		}
	}

	minutes := uint64(1)
	if senderAccount.EngagementLastHeartbeat != 0 {
		delta := payload.Timestamp - int64(senderAccount.EngagementLastHeartbeat)
		if delta > 0 {
			minutes = uint64(delta / int64(time.Minute/time.Second))
			if minutes == 0 {
				minutes = 1
			}
		}
	}
	if minutes > sp.engagementConfig.MaxMinutesPerHeartbeat {
		minutes = sp.engagementConfig.MaxMinutesPerHeartbeat
	}

	senderAccount.EngagementMinutes += minutes
	senderAccount.EngagementLastHeartbeat = uint64(payload.Timestamp)
	senderAccount.Nonce++
	if err := sp.setAccount(sender, senderAccount); err != nil {
		return err
	}
	sp.emitScoreUpdates(sender, updates)

	var addr [20]byte
	copy(addr[:], sender)
	evt := events.EngagementHeartbeat{
		Address:   addr,
		DeviceID:  payload.DeviceID,
		Minutes:   minutes,
		Timestamp: payload.Timestamp,
	}.Event()
	if evt != nil {
		sp.AppendEvent(evt)
	}

	fmt.Printf("Heartbeat processed: %s recorded %d minute(s).\n",
		crypto.NewAddress(crypto.NHBPrefix, sender).String(), minutes)
	return nil
}

type accountMetadata struct {
	BalanceZNHB             *big.Int
	Stake                   *big.Int
	Username                string
	EngagementScore         uint64
	EngagementDay           string
	EngagementMinutes       uint64
	EngagementTxCount       uint64
	EngagementEscrowEvents  uint64
	EngagementGovEvents     uint64
	EngagementLastHeartbeat uint64
}

func ensureAccountDefaults(account *types.Account) {
	if account.BalanceNHB == nil {
		account.BalanceNHB = big.NewInt(0)
	}
	if account.BalanceZNHB == nil {
		account.BalanceZNHB = big.NewInt(0)
	}
	if account.Stake == nil {
		account.Stake = big.NewInt(0)
	}
	if len(account.StorageRoot) == 0 {
		account.StorageRoot = gethtypes.EmptyRootHash.Bytes()
	}
	if len(account.CodeHash) == 0 {
		account.CodeHash = gethtypes.EmptyCodeHash.Bytes()
	}
}

// --- Helpers ---

func (sp *StateProcessor) getAccount(addr []byte) (*types.Account, error) {
	stateAcc, err := sp.loadStateAccount(addr)
	if err != nil {
		return nil, err
	}
	meta, err := sp.loadAccountMetadata(addr)
	if err != nil {
		return nil, err
	}

	account := &types.Account{
		BalanceNHB:              big.NewInt(0),
		BalanceZNHB:             big.NewInt(0),
		Stake:                   big.NewInt(0),
		EngagementScore:         0,
		EngagementDay:           "",
		EngagementMinutes:       0,
		EngagementTxCount:       0,
		EngagementEscrowEvents:  0,
		EngagementGovEvents:     0,
		EngagementLastHeartbeat: 0,
		StorageRoot:             gethtypes.EmptyRootHash.Bytes(),
		CodeHash:                gethtypes.EmptyCodeHash.Bytes(),
	}
	if stateAcc != nil {
		if stateAcc.Balance != nil {
			account.BalanceNHB = new(big.Int).Set(stateAcc.Balance.ToBig())
		}
		account.Nonce = stateAcc.Nonce
		account.StorageRoot = stateAcc.Root.Bytes()
		account.CodeHash = common.CopyBytes(stateAcc.CodeHash)
	}
	if meta != nil {
		account.BalanceZNHB = new(big.Int).Set(meta.BalanceZNHB)
		account.Stake = new(big.Int).Set(meta.Stake)
		account.Username = meta.Username
		account.EngagementScore = meta.EngagementScore
		account.EngagementDay = meta.EngagementDay
		account.EngagementMinutes = meta.EngagementMinutes
		account.EngagementTxCount = meta.EngagementTxCount
		account.EngagementEscrowEvents = meta.EngagementEscrowEvents
		account.EngagementGovEvents = meta.EngagementGovEvents
		account.EngagementLastHeartbeat = meta.EngagementLastHeartbeat
	}
	ensureAccountDefaults(account)
	return account, nil
}

func (sp *StateProcessor) setAccount(addr []byte, account *types.Account) error {
	if account == nil {
		return fmt.Errorf("nil account")
	}
	ensureAccountDefaults(account)

	prevMeta, err := sp.loadAccountMetadata(addr)
	if err != nil {
		return err
	}

	balance, overflow := uint256.FromBig(account.BalanceNHB)
	if overflow {
		return fmt.Errorf("balance overflow")
	}

	stateAcc := &gethtypes.StateAccount{
		Nonce:    account.Nonce,
		Balance:  balance,
		Root:     common.BytesToHash(account.StorageRoot),
		CodeHash: common.CopyBytes(account.CodeHash),
	}
	if len(stateAcc.CodeHash) == 0 {
		stateAcc.CodeHash = gethtypes.EmptyCodeHash.Bytes()
	}
	if stateAcc.Root == (common.Hash{}) {
		stateAcc.Root = gethtypes.EmptyRootHash
	}

	if err := sp.writeStateAccount(addr, stateAcc); err != nil {
		return err
	}

	meta := &accountMetadata{
		BalanceZNHB:             new(big.Int).Set(account.BalanceZNHB),
		Stake:                   new(big.Int).Set(account.Stake),
		Username:                account.Username,
		EngagementScore:         account.EngagementScore,
		EngagementDay:           account.EngagementDay,
		EngagementMinutes:       account.EngagementMinutes,
		EngagementTxCount:       account.EngagementTxCount,
		EngagementEscrowEvents:  account.EngagementEscrowEvents,
		EngagementGovEvents:     account.EngagementGovEvents,
		EngagementLastHeartbeat: account.EngagementLastHeartbeat,
	}
	if err := sp.writeAccountMetadata(addr, meta); err != nil {
		return err
	}

	prevUsername := ""
	prevStake := big.NewInt(0)
	if prevMeta != nil {
		prevUsername = prevMeta.Username
		if prevMeta.Stake != nil {
			prevStake = new(big.Int).Set(prevMeta.Stake)
		}
	}

	if prevUsername != "" && prevUsername != account.Username {
		delete(sp.usernameToAddr, prevUsername)
	}
	if account.Username != "" {
		sp.usernameToAddr[account.Username] = append([]byte(nil), addr...)
	}
	if err := sp.persistUsernameIndex(); err != nil {
		return err
	}

	minStake := big.NewInt(MINIMUM_STAKE)
	if prevStake.Cmp(minStake) >= 0 && account.Stake.Cmp(minStake) < 0 {
		delete(sp.ValidatorSet, string(addr))
	}
	if account.Stake.Cmp(minStake) >= 0 {
		sp.ValidatorSet[string(addr)] = new(big.Int).Set(account.Stake)
	} else if account.Stake.Cmp(minStake) < 0 {
		delete(sp.ValidatorSet, string(addr))
	}
	if err := sp.persistValidatorSet(); err != nil {
		return err
	}

	return nil
}

type engagementScoreUpdate struct {
	Day string
	Raw uint64
	Old uint64
	New uint64
}

func (sp *StateProcessor) computeRawEngagement(minutes, tx, escrow, gov uint64) uint64 {
	cfg := sp.engagementConfig
	total := new(big.Int)
	tmp := new(big.Int)

	if cfg.HeartbeatWeight > 0 && minutes > 0 {
		tmp.SetUint64(minutes)
		tmp.Mul(tmp, new(big.Int).SetUint64(cfg.HeartbeatWeight))
		total.Add(total, tmp)
	}
	if cfg.TxWeight > 0 && tx > 0 {
		tmp.SetUint64(tx)
		tmp.Mul(tmp, new(big.Int).SetUint64(cfg.TxWeight))
		total.Add(total, tmp)
	}
	if cfg.EscrowWeight > 0 && escrow > 0 {
		tmp.SetUint64(escrow)
		tmp.Mul(tmp, new(big.Int).SetUint64(cfg.EscrowWeight))
		total.Add(total, tmp)
	}
	if cfg.GovWeight > 0 && gov > 0 {
		tmp.SetUint64(gov)
		tmp.Mul(tmp, new(big.Int).SetUint64(cfg.GovWeight))
		total.Add(total, tmp)
	}

	if total.BitLen() > 64 {
		return ^uint64(0)
	}
	return total.Uint64()
}

func (sp *StateProcessor) applyEMAScore(prev, raw uint64) uint64 {
	cfg := sp.engagementConfig
	if cfg.LambdaDenominator == 0 {
		return raw
	}
	prevComponent := new(big.Int).SetUint64(prev)
	prevComponent.Mul(prevComponent, new(big.Int).SetUint64(cfg.LambdaNumerator))

	contribution := cfg.LambdaDenominator - cfg.LambdaNumerator
	rawComponent := new(big.Int).SetUint64(raw)
	rawComponent.Mul(rawComponent, new(big.Int).SetUint64(contribution))

	prevComponent.Add(prevComponent, rawComponent)
	prevComponent.Div(prevComponent, new(big.Int).SetUint64(cfg.LambdaDenominator))

	if prevComponent.BitLen() > 64 {
		return ^uint64(0)
	}
	return prevComponent.Uint64()
}

func (sp *StateProcessor) rolloverEngagement(account *types.Account, now time.Time) []engagementScoreUpdate {
	currentDay := now.UTC().Format(engagementDayFormat)
	if account.EngagementDay == "" {
		account.EngagementDay = currentDay
		return nil
	}
	if account.EngagementDay == currentDay {
		return nil
	}
	startDay, err := time.Parse(engagementDayFormat, account.EngagementDay)
	if err != nil {
		account.EngagementDay = currentDay
		account.EngagementMinutes = 0
		account.EngagementTxCount = 0
		account.EngagementEscrowEvents = 0
		account.EngagementGovEvents = 0
		return nil
	}
	targetDay, err := time.Parse(engagementDayFormat, currentDay)
	if err != nil {
		return nil
	}

	updates := make([]engagementScoreUpdate, 0)
	dayCursor := startDay
	for dayCursor.Before(targetDay) {
		raw := sp.computeRawEngagement(account.EngagementMinutes, account.EngagementTxCount, account.EngagementEscrowEvents, account.EngagementGovEvents)
		if raw > sp.engagementConfig.DailyCap {
			raw = sp.engagementConfig.DailyCap
		}
		oldScore := account.EngagementScore
		newScore := sp.applyEMAScore(oldScore, raw)
		updates = append(updates, engagementScoreUpdate{
			Day: dayCursor.Format(engagementDayFormat),
			Raw: raw,
			Old: oldScore,
			New: newScore,
		})
		account.EngagementScore = newScore
		account.EngagementMinutes = 0
		account.EngagementTxCount = 0
		account.EngagementEscrowEvents = 0
		account.EngagementGovEvents = 0
		dayCursor = dayCursor.AddDate(0, 0, 1)
	}
	account.EngagementDay = currentDay
	return updates
}

func (sp *StateProcessor) emitScoreUpdates(addr []byte, updates []engagementScoreUpdate) {
	if len(updates) == 0 {
		return
	}
	var address [20]byte
	copy(address[:], addr)
	for _, upd := range updates {
		evt := events.EngagementScoreUpdated{
			Address:  address,
			Day:      upd.Day,
			RawScore: upd.Raw,
			OldScore: upd.Old,
			NewScore: upd.New,
		}.Event()
		if evt != nil {
			sp.AppendEvent(evt)
		}
	}
}

func (sp *StateProcessor) recordEngagementActivity(addr []byte, now time.Time, txDelta, escrowDelta, govDelta uint64) error {
	if txDelta == 0 && escrowDelta == 0 && govDelta == 0 {
		return nil
	}
	account, err := sp.getAccount(addr)
	if err != nil {
		return err
	}
	updates := sp.rolloverEngagement(account, now)
	if txDelta > 0 {
		account.EngagementTxCount += txDelta
	}
	if escrowDelta > 0 {
		account.EngagementEscrowEvents += escrowDelta
	}
	if govDelta > 0 {
		account.EngagementGovEvents += govDelta
	}
	if err := sp.setAccount(addr, account); err != nil {
		return err
	}
	sp.emitScoreUpdates(addr, updates)
	return nil
}

func accountStateKey(addr []byte) []byte {
	return ethcrypto.Keccak256(addr)
}

func accountMetadataKey(addr []byte) []byte {
	buf := make([]byte, len(accountMetadataPrefix)+len(addr))
	copy(buf, accountMetadataPrefix)
	copy(buf[len(accountMetadataPrefix):], addr)
	return ethcrypto.Keccak256(buf)
}

func (sp *StateProcessor) loadStateAccount(addr []byte) (*gethtypes.StateAccount, error) {
	key := accountStateKey(addr)
	data, err := sp.Trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	stateAcc := new(gethtypes.StateAccount)
	if err := rlp.DecodeBytes(data, stateAcc); err != nil {
		slim := new(gethtypes.SlimAccount)
		if errSlim := rlp.DecodeBytes(data, slim); errSlim == nil {
			restored := &gethtypes.StateAccount{
				Nonce:   slim.Nonce,
				Balance: slim.Balance,
				Root:    gethtypes.EmptyRootHash,
				CodeHash: func() []byte {
					if len(slim.CodeHash) == 0 {
						return gethtypes.EmptyCodeHash.Bytes()
					}
					return append([]byte(nil), slim.CodeHash...)
				}(),
			}
			if len(slim.Root) != 0 {
				restored.Root = common.BytesToHash(slim.Root)
			}
			return restored, nil
		}
		legacy := new(types.Account)
		if errLegacy := rlp.DecodeBytes(data, legacy); errLegacy != nil {
			return nil, err
		}
		migrated, migrateErr := sp.migrateLegacyAccount(addr, legacy)
		if migrateErr != nil {
			return nil, migrateErr
		}
		return migrated, nil
	}
	return stateAcc, nil
}

func (sp *StateProcessor) migrateLegacyAccount(addr []byte, legacy *types.Account) (*gethtypes.StateAccount, error) {
	ensureAccountDefaults(legacy)

	balance, overflow := uint256.FromBig(legacy.BalanceNHB)
	if overflow {
		return nil, fmt.Errorf("balance overflow")
	}

	stateAcc := &gethtypes.StateAccount{
		Nonce:    legacy.Nonce,
		Balance:  balance,
		Root:     common.BytesToHash(legacy.StorageRoot),
		CodeHash: common.CopyBytes(legacy.CodeHash),
	}
	if len(stateAcc.CodeHash) == 0 {
		stateAcc.CodeHash = gethtypes.EmptyCodeHash.Bytes()
	}
	if stateAcc.Root == (common.Hash{}) {
		stateAcc.Root = gethtypes.EmptyRootHash
	}
	if err := sp.writeStateAccount(addr, stateAcc); err != nil {
		return nil, err
	}

	meta := &accountMetadata{
		BalanceZNHB:     new(big.Int).Set(legacy.BalanceZNHB),
		Stake:           new(big.Int).Set(legacy.Stake),
		Username:        legacy.Username,
		EngagementScore: legacy.EngagementScore,
	}
	if err := sp.writeAccountMetadata(addr, meta); err != nil {
		return nil, err
	}

	if legacy.Username != "" {
		sp.usernameToAddr[legacy.Username] = append([]byte(nil), addr...)
		if err := sp.persistUsernameIndex(); err != nil {
			return nil, err
		}
	}
	minStake := big.NewInt(MINIMUM_STAKE)
	if legacy.Stake.Cmp(minStake) >= 0 {
		sp.ValidatorSet[string(addr)] = new(big.Int).Set(legacy.Stake)
		if err := sp.persistValidatorSet(); err != nil {
			return nil, err
		}
	}
	return stateAcc, nil
}

func (sp *StateProcessor) writeStateAccount(addr []byte, stateAcc *gethtypes.StateAccount) error {
	key := accountStateKey(addr)
	encoded, err := rlp.EncodeToBytes(stateAcc)
	if err != nil {
		return err
	}
	return sp.Trie.Update(key, encoded)
}

func (sp *StateProcessor) loadAccountMetadata(addr []byte) (*accountMetadata, error) {
	key := accountMetadataKey(addr)
	data, err := sp.Trie.Get(key)
	if err != nil {
		return nil, err
	}
	meta := &accountMetadata{
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}
	if len(data) == 0 {
		return meta, nil
	}
	if err := rlp.DecodeBytes(data, meta); err != nil {
		return nil, err
	}
	if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
	}
	if meta.Stake == nil {
		meta.Stake = big.NewInt(0)
	}
	return meta, nil
}

func (sp *StateProcessor) writeAccountMetadata(addr []byte, meta *accountMetadata) error {
	if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
	}
	if meta.Stake == nil {
		meta.Stake = big.NewInt(0)
	}
	encoded, err := rlp.EncodeToBytes(meta)
	if err != nil {
		return err
	}
	return sp.Trie.Update(accountMetadataKey(addr), encoded)
}

type usernameIndexEntry struct {
	Username string
	Address  []byte
}

func (sp *StateProcessor) loadUsernameIndex() error {
	data, err := sp.Trie.Get(usernameIndexKey)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var entries []usernameIndexEntry
	if err := rlp.DecodeBytes(data, &entries); err != nil {
		return err
	}
	sp.usernameToAddr = make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.Username == "" {
			continue
		}
		sp.usernameToAddr[entry.Username] = append([]byte(nil), entry.Address...)
	}
	return nil
}

func (sp *StateProcessor) persistUsernameIndex() error {
	entries := make([]usernameIndexEntry, 0, len(sp.usernameToAddr))
	for username, addr := range sp.usernameToAddr {
		entries = append(entries, usernameIndexEntry{
			Username: username,
			Address:  append([]byte(nil), addr...),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Username < entries[j].Username })

	encoded, err := rlp.EncodeToBytes(entries)
	if err != nil {
		return err
	}
	return sp.Trie.Update(usernameIndexKey, encoded)
}

func (sp *StateProcessor) loadValidatorSet() error {
	data, err := sp.Trie.Get(validatorSetKey)
	if err != nil {
		return err
	}
	decoded, err := nhbstate.DecodeValidatorSet(data)
	if err != nil {
		return err
	}
	sp.ValidatorSet = make(map[string]*big.Int, len(decoded))
	for k, v := range decoded {
		if v == nil {
			v = big.NewInt(0)
		}
		sp.ValidatorSet[k] = new(big.Int).Set(v)
	}
	return nil
}

func (sp *StateProcessor) persistValidatorSet() error {
	encoded, err := nhbstate.EncodeValidatorSet(sp.ValidatorSet)
	if err != nil {
		return err
	}
	return sp.Trie.Update(validatorSetKey, encoded)
}

func (sp *StateProcessor) setEscrow(id []byte, e *escrow.LegacyEscrow) error {
	hashedKey := ethcrypto.Keccak256(append([]byte("escrow-"), id...))
	encoded, err := rlp.EncodeToBytes(e)
	if err != nil {
		return err
	}
	return sp.Trie.Update(hashedKey, encoded)
}

func (sp *StateProcessor) getEscrow(id []byte) (*escrow.LegacyEscrow, error) {
	hashedKey := ethcrypto.Keccak256(append([]byte("escrow-"), id...))
	data, err := sp.Trie.Get(hashedKey)
	if err != nil || data == nil {
		return nil, fmt.Errorf("escrow with ID %x not found", id)
	}
	e := new(escrow.LegacyEscrow)
	if err := rlp.DecodeBytes(data, e); err != nil {
		return nil, err
	}
	return e, nil
}

func (sp *StateProcessor) loadBigInt(key []byte) (*big.Int, error) {
	data, err := sp.Trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return big.NewInt(0), nil
	}
	value := new(big.Int)
	if err := rlp.DecodeBytes(data, value); err != nil {
		return nil, err
	}
	return value, nil
}

func (sp *StateProcessor) writeBigInt(key []byte, amount *big.Int) error {
	if amount == nil {
		amount = big.NewInt(0)
	}
	if amount.Sign() < 0 {
		return fmt.Errorf("negative value not allowed")
	}
	encoded, err := rlp.EncodeToBytes(amount)
	if err != nil {
		return err
	}
	return sp.Trie.Update(key, encoded)
}

func (sp *StateProcessor) PutAccount(addr []byte, account *types.Account) error {
	return sp.setAccount(addr, account)
}

func (sp *StateProcessor) LoyaltyGlobalConfig() (*loyalty.GlobalConfig, error) {
	key := nhbstate.LoyaltyGlobalStorageKey()
	data, err := sp.Trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	cfg := new(loyalty.GlobalConfig)
	if err := rlp.DecodeBytes(data, cfg); err != nil {
		return nil, err
	}
	return cfg.Normalize(), nil
}

func (sp *StateProcessor) LoyaltyBaseDailyAccrued(addr []byte, day string) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return nil, fmt.Errorf("day must not be empty")
	}
	key := nhbstate.LoyaltyBaseDailyMeterKey(addr, day)
	return sp.loadBigInt(key)
}

func (sp *StateProcessor) SetLoyaltyBaseDailyAccrued(addr []byte, day string, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return fmt.Errorf("day must not be empty")
	}
	key := nhbstate.LoyaltyBaseDailyMeterKey(addr, day)
	return sp.writeBigInt(key, amount)
}

func (sp *StateProcessor) LoyaltyBaseTotalAccrued(addr []byte) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	key := nhbstate.LoyaltyBaseTotalMeterKey(addr)
	return sp.loadBigInt(key)
}

func (sp *StateProcessor) SetLoyaltyBaseTotalAccrued(addr []byte, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	key := nhbstate.LoyaltyBaseTotalMeterKey(addr)
	return sp.writeBigInt(key, amount)
}

func (sp *StateProcessor) configureTradeEngine() (*escrow.TradeEngine, *nhbstate.Manager) {
	manager := nhbstate.NewManager(sp.Trie)
	if sp.EscrowEngine == nil {
		sp.EscrowEngine = escrow.NewEngine()
	}
	sp.EscrowEngine.SetState(manager)
	sp.EscrowEngine.SetEmitter(stateProcessorEmitter{sp: sp})
	if sp.TradeEngine == nil {
		sp.TradeEngine = escrow.NewTradeEngine(sp.EscrowEngine)
	}
	sp.TradeEngine.SetState(manager)
	sp.TradeEngine.SetEmitter(stateProcessorEmitter{sp: sp})
	return sp.TradeEngine, manager
}

type stateProcessorEmitter struct {
	sp *StateProcessor
}

func (e stateProcessorEmitter) Emit(evt events.Event) {
	if e.sp == nil || evt == nil {
		return
	}
	if provider, ok := evt.(interface{ Event() *types.Event }); ok {
		if payload := provider.Event(); payload != nil {
			e.sp.AppendEvent(payload)
		}
		return
	}
	e.sp.AppendEvent(&types.Event{Type: evt.EventType(), Attributes: map[string]string{}})
}

func (sp *StateProcessor) SettleTradeAtomic(tradeID [32]byte) error {
	tradeEngine, _ := sp.configureTradeEngine()
	return tradeEngine.SettleAtomic(tradeID)
}

func (sp *StateProcessor) TradeTryExpire(tradeID [32]byte, now int64) error {
	tradeEngine, _ := sp.configureTradeEngine()
	return tradeEngine.TradeTryExpire(tradeID, now)
}

func (sp *StateProcessor) OnTradeFundingProgress(tradeID [32]byte) error {
	tradeEngine, _ := sp.configureTradeEngine()
	return tradeEngine.OnFundingProgress(tradeID)
}

func (sp *StateProcessor) OnEscrowFunded(escrowID [32]byte) error {
	tradeEngine, _ := sp.configureTradeEngine()
	return tradeEngine.HandleEscrowFunded(escrowID)
}

func (sp *StateProcessor) AppendEvent(evt *types.Event) {
	if evt == nil {
		return
	}
	attrs := make(map[string]string, len(evt.Attributes))
	for k, v := range evt.Attributes {
		attrs[k] = v
	}
	sp.events = append(sp.events, types.Event{Type: evt.Type, Attributes: attrs})
}

func (sp *StateProcessor) Events() []types.Event {
	out := make([]types.Event, len(sp.events))
	for i := range sp.events {
		attrs := make(map[string]string, len(sp.events[i].Attributes))
		for k, v := range sp.events[i].Attributes {
			attrs[k] = v
		}
		out[i] = types.Event{Type: sp.events[i].Type, Attributes: attrs}
	}
	return out
}

func (sp *StateProcessor) GetAccount(addr []byte) (*types.Account, error) { return sp.getAccount(addr) }
func (sp *StateProcessor) IsValidator(addr []byte) bool {
	_, ok := sp.ValidatorSet[string(addr)]
	return ok
}

func (sp *StateProcessor) ResolveUsername(username string) ([]byte, bool) {
	trimmed := strings.TrimSpace(username)
	if trimmed == "" {
		return nil, false
	}
	addr, ok := sp.usernameToAddr[trimmed]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), addr...), true
}

func (sp *StateProcessor) HasRole(role string, addr []byte) bool {
	if len(addr) == 0 {
		return false
	}
	manager := nhbstate.NewManager(sp.Trie)
	return manager.HasRole(role, addr)
}

func (sp *StateProcessor) LoyaltyBusinessByID(id loyalty.BusinessID) (*loyalty.Business, bool, error) {
	manager := nhbstate.NewManager(sp.Trie)
	business := new(loyalty.Business)
	ok, err := manager.KVGet(nhbstate.LoyaltyBusinessKey(id), business)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return business, true, nil
}

func (sp *StateProcessor) LoyaltyProgramByID(id loyalty.ProgramID) (*loyalty.Program, bool, error) {
	manager := nhbstate.NewManager(sp.Trie)
	program := new(loyalty.Program)
	ok, err := manager.KVGet(loyalty.ProgramStorageKey(id), program)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return program, true, nil
}

func (sp *StateProcessor) LoyaltyProgramsByOwner(owner [20]byte) ([]loyalty.ProgramID, error) {
	manager := nhbstate.NewManager(sp.Trie)
	var raw [][]byte
	if err := manager.KVGetList(loyalty.ProgramOwnerIndexKey(owner), &raw); err != nil {
		return nil, err
	}
	ids := make([]loyalty.ProgramID, 0, len(raw))
	seen := make(map[[32]byte]struct{}, len(raw))
	for _, entry := range raw {
		if len(entry) != len(loyalty.ProgramID{}) {
			continue
		}
		var id loyalty.ProgramID
		copy(id[:], entry)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return bytes.Compare(ids[i][:], ids[j][:]) < 0 })
	return ids, nil
}

func (sp *StateProcessor) LoyaltyBusinessByMerchant(merchant [20]byte) (*loyalty.Business, bool, error) {
	manager := nhbstate.NewManager(sp.Trie)
	var id loyalty.BusinessID
	exists, err := manager.KVGet(nhbstate.LoyaltyMerchantIndexKey(merchant[:]), &id)
	if err != nil || !exists {
		if err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	if id == (loyalty.BusinessID{}) {
		return nil, false, nil
	}
	business := new(loyalty.Business)
	ok, err := manager.KVGet(nhbstate.LoyaltyBusinessKey(id), business)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return business, true, nil
}

func (sp *StateProcessor) LoyaltyProgramDailyAccrued(programID loyalty.ProgramID, addr []byte, day string) (*big.Int, error) {
	manager := nhbstate.NewManager(sp.Trie)
	return manager.LoyaltyProgramDailyAccrued(programID, addr, day)
}

func (sp *StateProcessor) SetLoyaltyProgramDailyAccrued(programID loyalty.ProgramID, addr []byte, day string, amount *big.Int) error {
	manager := nhbstate.NewManager(sp.Trie)
	return manager.SetLoyaltyProgramDailyAccrued(programID, addr, day, amount)
}
