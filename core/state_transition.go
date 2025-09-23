package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

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

// Privileged arbitrator address (replace with multisig in production).
var ARBITRATOR_ADDRESS = common.HexToAddress("0x00000000000000000000000000000000000000AA")

var (
	accountMetadataPrefix = []byte("account-meta:")
	usernameIndexKey      = ethcrypto.Keccak256([]byte("username-index"))
	validatorSetKey       = ethcrypto.Keccak256([]byte("validator-set"))
)

type StateProcessor struct {
	Trie           *trie.Trie
	stateDB        *gethstate.CachingDB
	LoyaltyEngine  *loyalty.Engine
	usernameToAddr map[string][]byte
	ValidatorSet   map[string]*big.Int
	committedRoot  common.Hash
	events         []types.Event
}

func NewStateProcessor(tr *trie.Trie) (*StateProcessor, error) {
	stateDB := gethstate.NewDatabase(tr.TrieDB(), nil)
	sp := &StateProcessor{
		Trie:           tr,
		stateDB:        stateDB,
		LoyaltyEngine:  loyalty.NewEngine(),
		usernameToAddr: make(map[string][]byte),
		ValidatorSet:   make(map[string]*big.Int),
		committedRoot:  tr.Root(),
		events:         make([]types.Event, 0),
	}
	if err := sp.loadUsernameIndex(); err != nil {
		return nil, err
	}
	if err := sp.loadValidatorSet(); err != nil {
		return nil, err
	}
	return sp, nil
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
		Trie:           trieCopy,
		stateDB:        sp.stateDB,
		LoyaltyEngine:  sp.LoyaltyEngine,
		usernameToAddr: usernameCopy,
		ValidatorSet:   validatorCopy,
		committedRoot:  sp.committedRoot,
		events:         eventsCopy,
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

	fmt.Printf("EVM transaction processed. Gas used: %d. Output: %x\n", result.UsedGas, result.ReturnData)
	return nil
}

// --- Native handlers (original semantics + new dispute flow) ---

func (sp *StateProcessor) handleNativeTransaction(tx *types.Transaction) error {
	sender, err := tx.From()
	if err != nil {
		return err
	}
	senderAccount, err := sp.getAccount(sender)
	if err != nil {
		return err
	}
	senderAccount.EngagementScore++
	if err := sp.setAccount(sender, senderAccount); err != nil {
		return err
	}

	switch tx.Type {
	case types.TxTypeRegisterIdentity:
		return sp.applyRegisterIdentity(tx)
	case types.TxTypeCreateEscrow:
		return sp.applyCreateEscrow(tx)
	case types.TxTypeReleaseEscrow:
		return sp.applyReleaseEscrow(tx)
	case types.TxTypeRefundEscrow:
		return sp.applyRefundEscrow(tx)
	case types.TxTypeStake:
		return sp.applyStake(tx)
	case types.TxTypeUnstake:
		return sp.applyUnstake(tx)
	case types.TxTypeHeartbeat:
		return sp.applyHeartbeat(tx)

	// --- NEW DISPUTE RESOLUTION CASES ---
	case types.TxTypeLockEscrow:
		return sp.applyLockEscrow(tx)
	case types.TxTypeDisputeEscrow:
		return sp.applyDisputeEscrow(tx)
	case types.TxTypeArbitrateRelease:
		return sp.applyArbitrate(tx, true)
	case types.TxTypeArbitrateRefund:
		return sp.applyArbitrate(tx, false)
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
	newEscrow := escrow.Escrow{
		ID:     escrowID,
		Seller: from, // The creator of the tx is always the seller with the asset
		Amount: escrowData.Amount,
	}

	// --- THE SYMMETRICAL ESCROW UPGRADE ---
	if escrowData.Buyer != nil {
		// This is a "Buy Offer" being accepted. The escrow starts locked.
		newEscrow.Buyer = escrowData.Buyer
		newEscrow.Status = escrow.StatusInProgress
		fmt.Printf("Symmetrical Escrow Created (In Progress): Seller %s locks funds for Buyer %s, Amount: %s, ID: %x\n",
			crypto.NewAddress(crypto.NHBPrefix, from).String(),
			crypto.NewAddress(crypto.NHBPrefix, escrowData.Buyer).String(),
			newEscrow.Amount.String(), newEscrow.ID)
	} else {
		// This is a standard "Sell Offer". The escrow starts open, and the seller is the initial "buyer".
		newEscrow.Buyer = from
		newEscrow.Status = escrow.StatusOpen
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
	if e.Status != escrow.StatusOpen {
		return fmt.Errorf("escrow not open")
	}
	if string(sender) != string(e.Buyer) {
		return fmt.Errorf("only buyer can release")
	}
	e.Status = escrow.StatusReleased
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
	if e.Status != escrow.StatusOpen {
		return fmt.Errorf("escrow not open")
	}
	if string(sender) != string(e.Seller) {
		return fmt.Errorf("only seller can refund")
	}
	e.Status = escrow.StatusRefunded
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

	if e.Status != escrow.StatusOpen {
		return fmt.Errorf("escrow is not open to be locked")
	}

	e.Buyer = sender
	e.Status = escrow.StatusInProgress

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

	if e.Status != escrow.StatusInProgress {
		return fmt.Errorf("only an in-progress escrow can be disputed")
	}
	if !bytes.Equal(sender, e.Buyer) {
		return fmt.Errorf("only the buyer can dispute an escrow")
	}

	e.Status = escrow.StatusDisputed

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

	if e.Status != escrow.StatusDisputed {
		return fmt.Errorf("escrow is not in a disputed state")
	}

	if releaseToBuyer {
		e.Status = escrow.StatusReleased
		buyerAccount, _ := sp.getAccount(e.Buyer)
		buyerAccount.BalanceNHB.Add(buyerAccount.BalanceNHB, e.Amount)
		if err := sp.setAccount(e.Buyer, buyerAccount); err != nil {
			return err
		}
		fmt.Printf("Arbitration complete: Escrow %x released to buyer.\n", escrowID)
	} else {
		e.Status = escrow.StatusRefunded
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
	sender, _ := tx.From()
	senderAccount, _ := sp.getAccount(sender)
	senderAccount.Nonce++
	sp.setAccount(sender, senderAccount)
	fmt.Printf("Heartbeat processed: Engagement score for %s incremented.\n",
		crypto.NewAddress(crypto.NHBPrefix, sender).String())
	return nil
}

type accountMetadata struct {
	BalanceZNHB     *big.Int
	Stake           *big.Int
	Username        string
	EngagementScore uint64
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
		BalanceNHB:      big.NewInt(0),
		BalanceZNHB:     big.NewInt(0),
		Stake:           big.NewInt(0),
		EngagementScore: 0,
		StorageRoot:     gethtypes.EmptyRootHash.Bytes(),
		CodeHash:        gethtypes.EmptyCodeHash.Bytes(),
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
		BalanceZNHB:     new(big.Int).Set(account.BalanceZNHB),
		Stake:           new(big.Int).Set(account.Stake),
		Username:        account.Username,
		EngagementScore: account.EngagementScore,
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

func (sp *StateProcessor) loadUsernameIndex() error {
	data, err := sp.Trie.Get(usernameIndexKey)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	stored := make(map[string][]byte)
	if err := rlp.DecodeBytes(data, &stored); err != nil {
		return err
	}
	sp.usernameToAddr = make(map[string][]byte, len(stored))
	for k, v := range stored {
		sp.usernameToAddr[k] = append([]byte(nil), v...)
	}
	return nil
}

func (sp *StateProcessor) persistUsernameIndex() error {
	encoded, err := rlp.EncodeToBytes(sp.usernameToAddr)
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

func (sp *StateProcessor) setEscrow(id []byte, e *escrow.Escrow) error {
	hashedKey := ethcrypto.Keccak256(append([]byte("escrow-"), id...))
	encoded, err := rlp.EncodeToBytes(e)
	if err != nil {
		return err
	}
	return sp.Trie.Update(hashedKey, encoded)
}

func (sp *StateProcessor) getEscrow(id []byte) (*escrow.Escrow, error) {
	hashedKey := ethcrypto.Keccak256(append([]byte("escrow-"), id...))
	data, err := sp.Trie.Get(hashedKey)
	if err != nil || data == nil {
		return nil, fmt.Errorf("escrow with ID %x not found", id)
	}
	e := new(escrow.Escrow)
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
