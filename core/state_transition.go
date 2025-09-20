package core

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
	"nhbchain/native/loyalty"
	"nhbchain/storage/trie"

	"github.com/ethereum/go-ethereum/common"
	gethcore "github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	gethvm "github.com/ethereum/go-ethereum/core/vm"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

const MINIMUM_STAKE = 1000

type StateProcessor struct {
	Trie           *trie.Trie
	LoyaltyEngine  *loyalty.Engine
	usernameToAddr map[string][]byte
	ValidatorSet   map[string]*big.Int
}

func NewStateProcessor(tr *trie.Trie) *StateProcessor {
	return &StateProcessor{
		Trie:           tr,
		LoyaltyEngine:  loyalty.NewEngine(),
		usernameToAddr: make(map[string][]byte),
		ValidatorSet:   make(map[string]*big.Int),
	}
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

	// 1) Build ephemeral Geth StateDB
	memdb := state.NewDatabaseForTesting()
	statedb, err := state.New(common.Hash{}, memdb)
	if err != nil {
		return fmt.Errorf("statedb init: %w", err)
	}

	// Seed helper: mirror balance & nonce from our trie -> statedb
	seed := func(addrBz []byte) (common.Address, *types.Account, error) {
		if addrBz == nil {
			return common.Address{}, nil, nil
		}
		addr := common.BytesToAddress(addrBz)
		acc, err := sp.getAccount(addrBz)
		if err != nil {
			return common.Address{}, nil, err
		}
		statedb.CreateAccount(addr)
		if acc.BalanceNHB == nil {
			acc.BalanceNHB = big.NewInt(0)
		}
		u, _ := uint256.FromBig(acc.BalanceNHB)
		statedb.SetBalance(addr, u, tracing.BalanceChangeUnspecified)
		statedb.SetNonce(addr, acc.Nonce, tracing.NonceChangeUnspecified)
		return addr, acc, nil
	}

	fromAddr, fromAcc, err := seed(from)
	if err != nil {
		return err
	}

	var toAddrPtr *common.Address
	var toAcc *types.Account
	if tx.To != nil {
		ta, taAcc, err := seed(tx.To)
		if err != nil {
			return err
		}
		toAddrPtr, toAcc = &ta, taAcc
	}

	// 2) Build contexts + message (struct literal)
	blockCtx := gethvm.BlockContext{
		Coinbase:    common.Address{},
		BlockNumber: big.NewInt(0),
		Time:        uint64(time.Now().Unix()),
		Difficulty:  big.NewInt(0),
	}

	msg := gethcore.Message{
		From:                  fromAddr,
		To:                    toAddrPtr,
		Nonce:                 tx.Nonce,
		Value:                 tx.Value,
		GasLimit:              tx.GasLimit,
		GasPrice:              tx.GasPrice,
		GasFeeCap:             tx.GasPrice, // simple: reuse
		GasTipCap:             tx.GasPrice, // simple: reuse
		Data:                  tx.Data,
		AccessList:            nil,
		BlobGasFeeCap:         nil,
		BlobHashes:            nil,
		SetCodeAuthorizations: nil,
		// NOTE: no SkipAccountChecks field in v1.16; do not set
	}
	txCtx := gethcore.NewEVMTxContext(&msg) // pointer expected

	// 3) NewEVM uses 4-arg signature; set tx context separately
	evm := gethvm.NewEVM(blockCtx, statedb, params.TestChainConfig, gethvm.Config{
		NoBaseFee: true, // disable basefee for this environment
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

	// 5) Sync balances & nonces back into our trie
	{
		u := statedb.GetBalance(fromAddr) // *uint256.Int
		fromAcc.BalanceNHB = new(big.Int).Set(u.ToBig())
		fromAcc.Nonce = statedb.GetNonce(fromAddr)
		if err := sp.setAccount(from, fromAcc); err != nil {
			return err
		}
	}
	if toAddrPtr != nil && toAcc != nil {
		u := statedb.GetBalance(*toAddrPtr)
		toAcc.BalanceNHB = new(big.Int).Set(u.ToBig())
		toAcc.Nonce = statedb.GetNonce(*toAddrPtr)
		if err := sp.setAccount(tx.To, toAcc); err != nil {
			return err
		}
	}

	// Native loyalty hook
	if tx.To != nil {
		sp.LoyaltyEngine.OnTransactionSuccess(fromAcc, toAcc)
	}

	fmt.Printf("EVM transaction processed. Gas used: %d. Output: %x\n", result.UsedGas, result.ReturnData)
	return nil
}

// --- Native handlers (same semantics as before) ---

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
	sp.usernameToAddr[username] = from
	sp.setAccount(from, fromAccount)
	fmt.Printf("Identity processed: Username '%s' registered to %s.\n",
		username, crypto.NewAddress(crypto.NHBPrefix, from).String())
	return nil
}

func (sp *StateProcessor) applyCreateEscrow(tx *types.Transaction) error {
	from, _ := tx.From()
	var escrowData struct {
		Seller []byte
		Amount *big.Int
	}
	if err := json.Unmarshal(tx.Data, &escrowData); err != nil {
		return fmt.Errorf("invalid escrow data: %w", err)
	}
	if escrowData.Amount == nil || escrowData.Amount.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("amount must be positive")
	}
	buyerAccount, _ := sp.getAccount(from)
	if buyerAccount.BalanceNHB.Cmp(escrowData.Amount) < 0 {
		return fmt.Errorf("insufficient funds")
	}
	buyerAccount.BalanceNHB.Sub(buyerAccount.BalanceNHB, escrowData.Amount)
	buyerAccount.Nonce++
	escrowID, _ := tx.Hash()
	newEscrow := escrow.Escrow{
		ID:     escrowID,
		Buyer:  from,
		Seller: escrowData.Seller,
		Amount: escrowData.Amount,
		Status: escrow.StatusOpen,
	}
	sp.setAccount(from, buyerAccount)
	sp.setEscrow(escrowID, &newEscrow)
	fmt.Printf("Escrow created: %s -> %s, Amount: %s, ID: %x\n",
		crypto.NewAddress(crypto.NHBPrefix, from).String(),
		crypto.NewAddress(crypto.NHBPrefix, escrowData.Seller).String(),
		newEscrow.Amount.String(), newEscrow.ID)
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

func (sp *StateProcessor) applyStake(tx *types.Transaction) error {
	sender, _ := tx.From()
	stakeAmount := tx.Value
	if stakeAmount == nil || stakeAmount.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("stake must be positive")
	}
	senderAccount, _ := sp.getAccount(sender)
	if senderAccount.BalanceZNHB.Cmp(stakeAmount) < 0 {
		return fmt.Errorf("insufficient ZapNHB")
	}
	senderAccount.BalanceZNHB.Sub(senderAccount.BalanceZNHB, stakeAmount)
	senderAccount.Stake.Add(senderAccount.Stake, stakeAmount)
	senderAccount.Nonce++
	sp.setAccount(sender, senderAccount)
	senderAddr := crypto.NewAddress(crypto.NHBPrefix, sender)
	if senderAccount.Stake.Cmp(big.NewInt(MINIMUM_STAKE)) >= 0 {
		sp.ValidatorSet[string(sender)] = senderAccount.Stake
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
	senderAccount, _ := sp.getAccount(sender)
	if senderAccount.Stake.Cmp(unStakeAmount) < 0 {
		return fmt.Errorf("insufficient staked balance")
	}
	senderAccount.Stake.Sub(senderAccount.Stake, unStakeAmount)
	senderAccount.BalanceZNHB.Add(senderAccount.BalanceZNHB, unStakeAmount)
	senderAccount.Nonce++
	sp.setAccount(sender, senderAccount)
	senderAddr := crypto.NewAddress(crypto.NHBPrefix, sender)
	if senderAccount.Stake.Cmp(big.NewInt(MINIMUM_STAKE)) < 0 {
		if _, ok := sp.ValidatorSet[string(sender)]; ok {
			delete(sp.ValidatorSet, string(sender))
			fmt.Printf("Account %s is no longer an active validator.\n", senderAddr.String())
		}
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

// --- Helpers ---

func (sp *StateProcessor) getAccount(addr []byte) (*types.Account, error) {
	key := ethcrypto.Keccak256(addr)
	data, _ := sp.Trie.Get(key)
	account := new(types.Account)
	if data != nil {
		if err := rlp.DecodeBytes(data, account); err != nil {
			return nil, err
		}
	} else {
		account.BalanceNHB = big.NewInt(0)
		account.BalanceZNHB = big.NewInt(0)
		account.Stake = big.NewInt(0)
		account.EngagementScore = 0
	}
	return account, nil
}
func (sp *StateProcessor) setAccount(addr []byte, account *types.Account) error {
	key := ethcrypto.Keccak256(addr)
	encoded, err := rlp.EncodeToBytes(account)
	if err != nil {
		return err
	}
	return sp.Trie.Put(key, encoded)
}
func (sp *StateProcessor) setEscrow(id []byte, e *escrow.Escrow) error {
	key := append([]byte("escrow-"), id...)
	encoded, err := rlp.EncodeToBytes(e)
	if err != nil {
		return err
	}
	return sp.Trie.Put(key, encoded)
}
func (sp *StateProcessor) getEscrow(id []byte) (*escrow.Escrow, error) {
	key := append([]byte("escrow-"), id...)
	data, err := sp.Trie.Get(key)
	if err != nil || data == nil {
		return nil, fmt.Errorf("escrow with ID %x not found", id)
	}
	e := new(escrow.Escrow)
	if err := rlp.DecodeBytes(data, e); err != nil {
		return nil, err
	}
	return e, nil
}
func (sp *StateProcessor) GetAccount(addr []byte) (*types.Account, error) {
	return sp.getAccount(addr)
}
func (sp *StateProcessor) IsValidator(addr []byte) bool {
	_, ok := sp.ValidatorSet[string(addr)]
	return ok
}
