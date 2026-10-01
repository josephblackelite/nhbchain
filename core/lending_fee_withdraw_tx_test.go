package core

import (
	"bytes"
	"errors"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// TestLendingFeeWithdrawTxTypeByteValues pins down the two new TxType byte
// values so a future merge can never silently reassign or collide them,
// mirroring TestSwapAdminTxTypeByteValues's precedent.
func TestLendingFeeWithdrawTxTypeByteValues(t *testing.T) {
	if types.TxTypeLendingWithdrawProtocolFees != 0x4D {
		t.Fatalf("expected TxTypeLendingWithdrawProtocolFees == 0x4D, got 0x%02X", byte(types.TxTypeLendingWithdrawProtocolFees))
	}
	if types.TxTypeLendingWithdrawDeveloperFees != 0x4E {
		t.Fatalf("expected TxTypeLendingWithdrawDeveloperFees == 0x4E, got 0x%02X", byte(types.TxTypeLendingWithdrawDeveloperFees))
	}
	if types.TxTypeLendingWithdrawProtocolFees == types.TxTypeLendingWithdrawDeveloperFees {
		t.Fatalf("TxTypeLendingWithdrawProtocolFees must not collide with TxTypeLendingWithdrawDeveloperFees")
	}
	// Senderless, like TxTypeSwapVoucherReverse/TxTypeSwapMarkReconciled --
	// see core/types/transaction.go's doc comment for why (the authorizing
	// key is not necessarily this node's own).
	if types.RequiresSignature(types.TxTypeLendingWithdrawProtocolFees) {
		t.Fatalf("expected TxTypeLendingWithdrawProtocolFees to be senderless (RequiresSignature=false)")
	}
	if types.RequiresSignature(types.TxTypeLendingWithdrawDeveloperFees) {
		t.Fatalf("expected TxTypeLendingWithdrawDeveloperFees to be senderless (RequiresSignature=false)")
	}
}

// TestLendingWithdrawProtocolFeesApply_DebitsAndTransfers is the direct
// regression test proving Engine.WithdrawProtocolFees (previously dead code
// with zero callers outside its own unit test -- ledger PL-DC-19) is now
// actually reachable end-to-end through a real, consensus-routed
// transaction, and correctly debits the pool's protocol fee accrual while
// crediting the admin-designated recipient.
func TestLendingWithdrawProtocolFeesApply_DebitsAndTransfers(t *testing.T) {
	node := newTestNode(t)

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	adminAddr := toAddress(adminKey)
	assignRole(t, node, RoleLendingProtocolAdmin, adminAddr)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipientAddr20 := toAddress(recipientKey)
	recipient := crypto.MustNewAddress(crypto.NHBPrefix, recipientAddr20[:])

	const poolID = "default"
	moduleAddr := node.LendingModuleAddress()

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.LendingPutMarket(poolID, &lending.Market{PoolID: poolID, TotalNHBSupplied: big.NewInt(500)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed market: %v", err)
	}
	if err := manager.LendingPutFeeAccrual(poolID, &lending.FeeAccrual{ProtocolFeesWei: big.NewInt(150), DeveloperFeesWei: big.NewInt(10)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed fee accrual: %v", err)
	}
	moduleAcc, err := manager.GetAccount(moduleAddr.Bytes())
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("load module account: %v", err)
	}
	moduleAcc.BalanceNHB = big.NewInt(400)
	if err := manager.PutAccount(moduleAddr.Bytes(), moduleAcc); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("fund module account: %v", err)
	}
	node.stateMu.Unlock()

	amount := big.NewInt(100)
	recipientStr := recipient.String()
	nonce := "withdraw-1"
	intentRef := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce)
	signature, err := ethcrypto.Sign(intentRef, adminKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal: %v", err)
	}
	payload, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, nonce, signature)
	if err != nil {
		t.Fatalf("encode withdrawal: %v", err)
	}
	tx := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeLendingWithdrawProtocolFees,
		Data:         payload,
		GasLimit:     0,
		GasPrice:     big.NewInt(0),
		IntentRef:    intentRef,
		IntentExpiry: uint64(time.Now().Add(time.Hour).Unix()),
	}

	node.stateMu.Lock()
	applyErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if applyErr != nil {
		t.Fatalf("apply protocol fee withdrawal: %v", applyErr)
	}

	node.stateMu.Lock()
	manager2 := nhbstate.NewManager(node.state.Trie)
	fees, ok, feesErr := manager2.LendingGetFeeAccrual(poolID)
	moduleAcc2, moduleErr := manager2.GetAccount(moduleAddr.Bytes())
	recipientAcc, recipientErr := manager2.GetAccount(recipient.Bytes())
	market, marketOk, marketErr := manager2.LendingGetMarket(poolID)
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual: ok=%v err=%v", ok, feesErr)
	}
	if fees.ProtocolFeesWei.Cmp(big.NewInt(50)) != 0 {
		t.Fatalf("expected protocol fees reduced to 50, got %s", fees.ProtocolFeesWei)
	}
	if fees.DeveloperFeesWei.Cmp(big.NewInt(10)) != 0 {
		t.Fatalf("expected developer fees untouched at 10, got %s", fees.DeveloperFeesWei)
	}
	if moduleErr != nil {
		t.Fatalf("read module account: %v", moduleErr)
	}
	if moduleAcc2.BalanceNHB.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("expected module balance debited to 300, got %s", moduleAcc2.BalanceNHB)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account: %v", recipientErr)
	}
	if recipientAcc.BalanceNHB.Cmp(amount) != 0 {
		t.Fatalf("expected recipient credited %s, got %s", amount, recipientAcc.BalanceNHB)
	}
	if marketErr != nil || !marketOk {
		t.Fatalf("read market: ok=%v err=%v", marketOk, marketErr)
	}
	if market.TotalNHBSupplied.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected market TotalNHBSupplied reduced to 400, got %s", market.TotalNHBSupplied)
	}
}

// TestLendingWithdrawProtocolFeesApply_RejectsNonAdminSigner is the direct
// regression test for the authorization half of the fix: a transaction
// signed by a key that was never granted RoleLendingProtocolAdmin must be
// rejected deterministically by applyLendingWithdrawProtocolFeesTransaction
// itself, before it ever touches the fee accrual or moves any balance.
func TestLendingWithdrawProtocolFeesApply_RejectsNonAdminSigner(t *testing.T) {
	node := newTestNode(t)

	// rogueKey is never granted RoleLendingProtocolAdmin.
	rogueKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate rogue key: %v", err)
	}

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipientAddr20 := toAddress(recipientKey)
	recipient := crypto.MustNewAddress(crypto.NHBPrefix, recipientAddr20[:])

	const poolID = "default"
	moduleAddr := node.LendingModuleAddress()

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.LendingPutMarket(poolID, &lending.Market{PoolID: poolID, TotalNHBSupplied: big.NewInt(500)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed market: %v", err)
	}
	if err := manager.LendingPutFeeAccrual(poolID, &lending.FeeAccrual{ProtocolFeesWei: big.NewInt(150), DeveloperFeesWei: big.NewInt(10)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed fee accrual: %v", err)
	}
	moduleAcc, err := manager.GetAccount(moduleAddr.Bytes())
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("load module account: %v", err)
	}
	moduleAcc.BalanceNHB = big.NewInt(400)
	if err := manager.PutAccount(moduleAddr.Bytes(), moduleAcc); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("fund module account: %v", err)
	}
	node.stateMu.Unlock()

	amount := big.NewInt(100)
	recipientStr := recipient.String()
	nonce := "withdraw-1"
	intentRef := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce)
	signature, err := ethcrypto.Sign(intentRef, rogueKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal: %v", err)
	}
	payload, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, nonce, signature)
	if err != nil {
		t.Fatalf("encode withdrawal: %v", err)
	}
	tx := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeLendingWithdrawProtocolFees,
		Data:         payload,
		GasLimit:     0,
		GasPrice:     big.NewInt(0),
		IntentRef:    intentRef,
		IntentExpiry: uint64(time.Now().Add(time.Hour).Unix()),
	}

	node.stateMu.Lock()
	applyErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if !errors.Is(applyErr, ErrLendingFeeWithdrawUnauthorized) {
		t.Fatalf("SECURITY REGRESSION: expected ErrLendingFeeWithdrawUnauthorized for a non-admin signer, got %v", applyErr)
	}

	node.stateMu.Lock()
	manager2 := nhbstate.NewManager(node.state.Trie)
	fees, ok, feesErr := manager2.LendingGetFeeAccrual(poolID)
	moduleAcc2, moduleErr := manager2.GetAccount(moduleAddr.Bytes())
	recipientAcc, recipientErr := manager2.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual: ok=%v err=%v", ok, feesErr)
	}
	if fees.ProtocolFeesWei.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("expected protocol fees unchanged at 150, got %s", fees.ProtocolFeesWei)
	}
	if moduleErr != nil {
		t.Fatalf("read module account: %v", moduleErr)
	}
	if moduleAcc2.BalanceNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected module balance unchanged at 400, got %s", moduleAcc2.BalanceNHB)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account: %v", recipientErr)
	}
	if recipientAcc.BalanceNHB.Sign() != 0 {
		t.Fatalf("expected recipient balance unchanged at 0, got %s", recipientAcc.BalanceNHB)
	}
}

// TestLendingWithdrawDeveloperFeesApply_DebitsAndTransfers is
// TestLendingWithdrawProtocolFeesApply_DebitsAndTransfers's counterpart for
// Engine.WithdrawDeveloperFees, authorized via the pool's own
// Market.DeveloperOwner rather than a registered role.
func TestLendingWithdrawDeveloperFeesApply_DebitsAndTransfers(t *testing.T) {
	node := newTestNode(t)

	developerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate developer key: %v", err)
	}
	developerAddr20 := toAddress(developerKey)
	developerOwner := crypto.MustNewAddress(crypto.NHBPrefix, developerAddr20[:])

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipientAddr20 := toAddress(recipientKey)
	recipient := crypto.MustNewAddress(crypto.NHBPrefix, recipientAddr20[:])

	const poolID = "default"
	moduleAddr := node.LendingModuleAddress()

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.LendingPutMarket(poolID, &lending.Market{PoolID: poolID, DeveloperOwner: developerOwner, TotalNHBSupplied: big.NewInt(500)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed market: %v", err)
	}
	if err := manager.LendingPutFeeAccrual(poolID, &lending.FeeAccrual{ProtocolFeesWei: big.NewInt(10), DeveloperFeesWei: big.NewInt(150)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed fee accrual: %v", err)
	}
	moduleAcc, err := manager.GetAccount(moduleAddr.Bytes())
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("load module account: %v", err)
	}
	moduleAcc.BalanceNHB = big.NewInt(400)
	if err := manager.PutAccount(moduleAddr.Bytes(), moduleAcc); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("fund module account: %v", err)
	}
	node.stateMu.Unlock()

	amount := big.NewInt(100)
	recipientStr := recipient.String()
	nonce := "withdraw-1"
	intentRef := LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce)
	signature, err := ethcrypto.Sign(intentRef, developerKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal: %v", err)
	}
	payload, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, nonce, signature)
	if err != nil {
		t.Fatalf("encode withdrawal: %v", err)
	}
	tx := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeLendingWithdrawDeveloperFees,
		Data:         payload,
		GasLimit:     0,
		GasPrice:     big.NewInt(0),
		IntentRef:    intentRef,
		IntentExpiry: uint64(time.Now().Add(time.Hour).Unix()),
	}

	node.stateMu.Lock()
	applyErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if applyErr != nil {
		t.Fatalf("apply developer fee withdrawal: %v", applyErr)
	}

	node.stateMu.Lock()
	manager2 := nhbstate.NewManager(node.state.Trie)
	fees, ok, feesErr := manager2.LendingGetFeeAccrual(poolID)
	moduleAcc2, moduleErr := manager2.GetAccount(moduleAddr.Bytes())
	recipientAcc, recipientErr := manager2.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual: ok=%v err=%v", ok, feesErr)
	}
	if fees.DeveloperFeesWei.Cmp(big.NewInt(50)) != 0 {
		t.Fatalf("expected developer fees reduced to 50, got %s", fees.DeveloperFeesWei)
	}
	if fees.ProtocolFeesWei.Cmp(big.NewInt(10)) != 0 {
		t.Fatalf("expected protocol fees untouched at 10, got %s", fees.ProtocolFeesWei)
	}
	if moduleErr != nil {
		t.Fatalf("read module account: %v", moduleErr)
	}
	if moduleAcc2.BalanceNHB.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("expected module balance debited to 300, got %s", moduleAcc2.BalanceNHB)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account: %v", recipientErr)
	}
	if recipientAcc.BalanceNHB.Cmp(amount) != 0 {
		t.Fatalf("expected recipient credited %s, got %s", amount, recipientAcc.BalanceNHB)
	}
}

// TestLendingWithdrawDeveloperFeesApply_RejectsNonOwnerSigner is
// TestLendingWithdrawProtocolFeesApply_RejectsNonAdminSigner's counterpart:
// a transaction signed by a key that is not the pool's Market.DeveloperOwner
// -- even one holding RoleLendingProtocolAdmin -- must be rejected before it
// ever touches the fee accrual or moves any balance.
func TestLendingWithdrawDeveloperFeesApply_RejectsNonOwnerSigner(t *testing.T) {
	node := newTestNode(t)

	developerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate developer key: %v", err)
	}
	developerAddr20 := toAddress(developerKey)
	developerOwner := crypto.MustNewAddress(crypto.NHBPrefix, developerAddr20[:])

	// rogueKey holds RoleLendingProtocolAdmin (the OTHER withdrawal's
	// authority) but is not this pool's DeveloperOwner -- confirms the two
	// authorities are not interchangeable.
	rogueKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate rogue key: %v", err)
	}
	rogueAddr := toAddress(rogueKey)
	assignRole(t, node, RoleLendingProtocolAdmin, rogueAddr)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipientAddr20 := toAddress(recipientKey)
	recipient := crypto.MustNewAddress(crypto.NHBPrefix, recipientAddr20[:])

	const poolID = "default"
	moduleAddr := node.LendingModuleAddress()

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.LendingPutMarket(poolID, &lending.Market{PoolID: poolID, DeveloperOwner: developerOwner, TotalNHBSupplied: big.NewInt(500)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed market: %v", err)
	}
	if err := manager.LendingPutFeeAccrual(poolID, &lending.FeeAccrual{ProtocolFeesWei: big.NewInt(10), DeveloperFeesWei: big.NewInt(150)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed fee accrual: %v", err)
	}
	moduleAcc, err := manager.GetAccount(moduleAddr.Bytes())
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("load module account: %v", err)
	}
	moduleAcc.BalanceNHB = big.NewInt(400)
	if err := manager.PutAccount(moduleAddr.Bytes(), moduleAcc); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("fund module account: %v", err)
	}
	node.stateMu.Unlock()

	amount := big.NewInt(100)
	recipientStr := recipient.String()
	nonce := "withdraw-1"
	intentRef := LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce)
	signature, err := ethcrypto.Sign(intentRef, rogueKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal: %v", err)
	}
	payload, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, nonce, signature)
	if err != nil {
		t.Fatalf("encode withdrawal: %v", err)
	}
	tx := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeLendingWithdrawDeveloperFees,
		Data:         payload,
		GasLimit:     0,
		GasPrice:     big.NewInt(0),
		IntentRef:    intentRef,
		IntentExpiry: uint64(time.Now().Add(time.Hour).Unix()),
	}

	node.stateMu.Lock()
	applyErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if !errors.Is(applyErr, ErrLendingFeeWithdrawUnauthorized) {
		t.Fatalf("SECURITY REGRESSION: expected ErrLendingFeeWithdrawUnauthorized for a non-owner signer, got %v", applyErr)
	}

	node.stateMu.Lock()
	manager2 := nhbstate.NewManager(node.state.Trie)
	fees, ok, feesErr := manager2.LendingGetFeeAccrual(poolID)
	moduleAcc2, moduleErr := manager2.GetAccount(moduleAddr.Bytes())
	recipientAcc, recipientErr := manager2.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual: ok=%v err=%v", ok, feesErr)
	}
	if fees.DeveloperFeesWei.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("expected developer fees unchanged at 150, got %s", fees.DeveloperFeesWei)
	}
	if moduleErr != nil {
		t.Fatalf("read module account: %v", moduleErr)
	}
	if moduleAcc2.BalanceNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected module balance unchanged at 400, got %s", moduleAcc2.BalanceNHB)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account: %v", recipientErr)
	}
	if recipientAcc.BalanceNHB.Sign() != 0 {
		t.Fatalf("expected recipient balance unchanged at 0, got %s", recipientAcc.BalanceNHB)
	}
}

// TestLendingWithdrawProtocolFeesApply_RejectsReplayAllowsNewAuthorization is
// the direct regression test for the NHB-AUDIT replay vulnerability: two
// independent adversarial reviews proved that
// LendingProtocolFeeWithdrawSigningHash's signed payload (poolId|recipient|
// amountWei, with no nonce/sequence/expiry/one-time-use token) let the
// byte-identical signed transaction be resubmitted and silently re-executed
// every time the pool re-accrued at least as much again -- a PoC replayed one
// signed withdrawal 3 times, draining 3x the single authorized amount.
//
// This proves both halves of the fix:
//  1. Resubmitting the EXACT SAME signed transaction a second time, after the
//     pool has re-accrued enough fees to make the drain possible again, now
//     fails with core/state/intent_registry.go's ErrIntentConsumed instead of
//     silently succeeding again -- and leaves the fee accrual/balances
//     completely untouched (the generic intent check in
//     core/state_transition.go's ApplyTransaction runs BEFORE
//     applyLendingWithdrawProtocolFeesTransaction is ever dispatched).
//  2. The SAME admin authorizing a genuinely NEW withdrawal (a fresh nonce,
//     exactly as LendingProtocolFeeWithdrawSigningHash's doc comment
//     requires) still succeeds normally -- the fix does not block every
//     future withdrawal from an admin who has already withdrawn once.
func TestLendingWithdrawProtocolFeesApply_RejectsReplayAllowsNewAuthorization(t *testing.T) {
	node := newTestNode(t)

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	adminAddr := toAddress(adminKey)
	assignRole(t, node, RoleLendingProtocolAdmin, adminAddr)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipientAddr20 := toAddress(recipientKey)
	recipient := crypto.MustNewAddress(crypto.NHBPrefix, recipientAddr20[:])
	recipientStr := recipient.String()

	const poolID = "default"
	moduleAddr := node.LendingModuleAddress()
	amount := big.NewInt(100)

	seedFees := func(protocolFeesWei int64) {
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		manager := nhbstate.NewManager(node.state.Trie)
		if err := manager.LendingPutFeeAccrual(poolID, &lending.FeeAccrual{ProtocolFeesWei: big.NewInt(protocolFeesWei), DeveloperFeesWei: big.NewInt(0)}); err != nil {
			t.Fatalf("seed fee accrual: %v", err)
		}
	}

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.LendingPutMarket(poolID, &lending.Market{PoolID: poolID, TotalNHBSupplied: big.NewInt(1000)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed market: %v", err)
	}
	moduleAcc, err := manager.GetAccount(moduleAddr.Bytes())
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("load module account: %v", err)
	}
	moduleAcc.BalanceNHB = big.NewInt(500)
	if err := manager.PutAccount(moduleAddr.Bytes(), moduleAcc); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("fund module account: %v", err)
	}
	node.stateMu.Unlock()
	seedFees(100)

	// --- First, legitimate withdrawal: nonce "withdraw-1". ---
	nonce1 := "withdraw-1"
	intentRef1 := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce1)
	sig1, err := ethcrypto.Sign(intentRef1, adminKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal 1: %v", err)
	}
	payload1, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, nonce1, sig1)
	if err != nil {
		t.Fatalf("encode withdrawal 1: %v", err)
	}
	tx1 := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeLendingWithdrawProtocolFees,
		Data:         payload1,
		GasLimit:     0,
		GasPrice:     big.NewInt(0),
		IntentRef:    intentRef1,
		IntentExpiry: uint64(time.Now().Add(time.Hour).Unix()),
	}

	node.stateMu.Lock()
	err = node.state.ApplyTransaction(tx1)
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("apply first withdrawal: %v", err)
	}

	// The pool re-accrues fees over time -- exactly the precondition the
	// verifier's PoC relied on to make each replay drain funds again.
	seedFees(100)

	// --- BEFORE the fix this resubmission would silently succeed again and
	// drain a second 100; AFTER the fix it must fail with ErrIntentConsumed
	// and touch no state. ---
	node.stateMu.Lock()
	replayErr := node.state.ApplyTransaction(tx1)
	node.stateMu.Unlock()
	if !errors.Is(replayErr, nhbstate.ErrIntentConsumed) {
		t.Fatalf("SECURITY REGRESSION: expected ErrIntentConsumed replaying an already-consumed withdrawal, got %v", replayErr)
	}

	node.stateMu.Lock()
	managerAfterReplay := nhbstate.NewManager(node.state.Trie)
	feesAfterReplay, ok, feesErr := managerAfterReplay.LendingGetFeeAccrual(poolID)
	recipientAfterReplay, recipientErr := managerAfterReplay.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual after replay: ok=%v err=%v", ok, feesErr)
	}
	if feesAfterReplay.ProtocolFeesWei.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("SECURITY REGRESSION: replay must not touch fee accrual, got %s", feesAfterReplay.ProtocolFeesWei)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account after replay: %v", recipientErr)
	}
	if recipientAfterReplay.BalanceNHB.Cmp(amount) != 0 {
		t.Fatalf("SECURITY REGRESSION: replay must not credit the recipient again, got %s", recipientAfterReplay.BalanceNHB)
	}

	// --- A genuinely NEW withdrawal from the SAME admin, authorized with a
	// fresh nonce, must still succeed -- the fix must not over-correct into
	// blocking every future withdrawal from an admin who has withdrawn
	// before. ---
	nonce2 := "withdraw-2"
	intentRef2 := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce2)
	if bytes.Equal(intentRef1, intentRef2) {
		t.Fatalf("test setup error: nonce1/nonce2 must produce distinct intent refs")
	}
	sig2, err := ethcrypto.Sign(intentRef2, adminKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal 2: %v", err)
	}
	payload2, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, nonce2, sig2)
	if err != nil {
		t.Fatalf("encode withdrawal 2: %v", err)
	}
	tx2 := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeLendingWithdrawProtocolFees,
		Data:         payload2,
		GasLimit:     0,
		GasPrice:     big.NewInt(0),
		IntentRef:    intentRef2,
		IntentExpiry: uint64(time.Now().Add(time.Hour).Unix()),
	}

	node.stateMu.Lock()
	err = node.state.ApplyTransaction(tx2)
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("apply second, newly-authorized withdrawal: %v", err)
	}

	node.stateMu.Lock()
	managerFinal := nhbstate.NewManager(node.state.Trie)
	feesFinal, ok, feesErr := managerFinal.LendingGetFeeAccrual(poolID)
	recipientFinal, recipientErr := managerFinal.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual after second withdrawal: ok=%v err=%v", ok, feesErr)
	}
	if feesFinal.ProtocolFeesWei.Sign() != 0 {
		t.Fatalf("expected protocol fees drained to 0 after the second withdrawal, got %s", feesFinal.ProtocolFeesWei)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account after second withdrawal: %v", recipientErr)
	}
	wantTotal := new(big.Int).Mul(amount, big.NewInt(2))
	if recipientFinal.BalanceNHB.Cmp(wantTotal) != 0 {
		t.Fatalf("expected recipient credited %s total across both withdrawals, got %s", wantTotal, recipientFinal.BalanceNHB)
	}
}

// TestLendingWithdrawDeveloperFeesApply_RejectsReplayAllowsNewAuthorization is
// TestLendingWithdrawProtocolFeesApply_RejectsReplayAllowsNewAuthorization's
// counterpart for TxTypeLendingWithdrawDeveloperFees -- the verifier's
// finding named LendingDeveloperFeeWithdrawSigningHash explicitly alongside
// LendingProtocolFeeWithdrawSigningHash as sharing the identical
// no-nonce/no-replay-protection defect, so both tx types need the identical
// regression coverage.
func TestLendingWithdrawDeveloperFeesApply_RejectsReplayAllowsNewAuthorization(t *testing.T) {
	node := newTestNode(t)

	developerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate developer key: %v", err)
	}
	developerAddr20 := toAddress(developerKey)
	developerOwner := crypto.MustNewAddress(crypto.NHBPrefix, developerAddr20[:])

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipientAddr20 := toAddress(recipientKey)
	recipient := crypto.MustNewAddress(crypto.NHBPrefix, recipientAddr20[:])
	recipientStr := recipient.String()

	const poolID = "default"
	moduleAddr := node.LendingModuleAddress()
	amount := big.NewInt(100)

	seedFees := func(developerFeesWei int64) {
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		manager := nhbstate.NewManager(node.state.Trie)
		if err := manager.LendingPutFeeAccrual(poolID, &lending.FeeAccrual{ProtocolFeesWei: big.NewInt(0), DeveloperFeesWei: big.NewInt(developerFeesWei)}); err != nil {
			t.Fatalf("seed fee accrual: %v", err)
		}
	}

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.LendingPutMarket(poolID, &lending.Market{PoolID: poolID, DeveloperOwner: developerOwner, TotalNHBSupplied: big.NewInt(1000)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed market: %v", err)
	}
	moduleAcc, err := manager.GetAccount(moduleAddr.Bytes())
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("load module account: %v", err)
	}
	moduleAcc.BalanceNHB = big.NewInt(500)
	if err := manager.PutAccount(moduleAddr.Bytes(), moduleAcc); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("fund module account: %v", err)
	}
	node.stateMu.Unlock()
	seedFees(100)

	nonce1 := "withdraw-1"
	intentRef1 := LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce1)
	sig1, err := ethcrypto.Sign(intentRef1, developerKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal 1: %v", err)
	}
	payload1, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, nonce1, sig1)
	if err != nil {
		t.Fatalf("encode withdrawal 1: %v", err)
	}
	tx1 := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeLendingWithdrawDeveloperFees,
		Data:         payload1,
		GasLimit:     0,
		GasPrice:     big.NewInt(0),
		IntentRef:    intentRef1,
		IntentExpiry: uint64(time.Now().Add(time.Hour).Unix()),
	}

	node.stateMu.Lock()
	err = node.state.ApplyTransaction(tx1)
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("apply first withdrawal: %v", err)
	}

	seedFees(100)

	node.stateMu.Lock()
	replayErr := node.state.ApplyTransaction(tx1)
	node.stateMu.Unlock()
	if !errors.Is(replayErr, nhbstate.ErrIntentConsumed) {
		t.Fatalf("SECURITY REGRESSION: expected ErrIntentConsumed replaying an already-consumed withdrawal, got %v", replayErr)
	}

	node.stateMu.Lock()
	managerAfterReplay := nhbstate.NewManager(node.state.Trie)
	feesAfterReplay, ok, feesErr := managerAfterReplay.LendingGetFeeAccrual(poolID)
	recipientAfterReplay, recipientErr := managerAfterReplay.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual after replay: ok=%v err=%v", ok, feesErr)
	}
	if feesAfterReplay.DeveloperFeesWei.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("SECURITY REGRESSION: replay must not touch fee accrual, got %s", feesAfterReplay.DeveloperFeesWei)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account after replay: %v", recipientErr)
	}
	if recipientAfterReplay.BalanceNHB.Cmp(amount) != 0 {
		t.Fatalf("SECURITY REGRESSION: replay must not credit the recipient again, got %s", recipientAfterReplay.BalanceNHB)
	}

	nonce2 := "withdraw-2"
	intentRef2 := LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce2)
	if bytes.Equal(intentRef1, intentRef2) {
		t.Fatalf("test setup error: nonce1/nonce2 must produce distinct intent refs")
	}
	sig2, err := ethcrypto.Sign(intentRef2, developerKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal 2: %v", err)
	}
	payload2, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, nonce2, sig2)
	if err != nil {
		t.Fatalf("encode withdrawal 2: %v", err)
	}
	tx2 := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeLendingWithdrawDeveloperFees,
		Data:         payload2,
		GasLimit:     0,
		GasPrice:     big.NewInt(0),
		IntentRef:    intentRef2,
		IntentExpiry: uint64(time.Now().Add(time.Hour).Unix()),
	}

	node.stateMu.Lock()
	err = node.state.ApplyTransaction(tx2)
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("apply second, newly-authorized withdrawal: %v", err)
	}

	node.stateMu.Lock()
	managerFinal := nhbstate.NewManager(node.state.Trie)
	feesFinal, ok, feesErr := managerFinal.LendingGetFeeAccrual(poolID)
	recipientFinal, recipientErr := managerFinal.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual after second withdrawal: ok=%v err=%v", ok, feesErr)
	}
	if feesFinal.DeveloperFeesWei.Sign() != 0 {
		t.Fatalf("expected developer fees drained to 0 after the second withdrawal, got %s", feesFinal.DeveloperFeesWei)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account after second withdrawal: %v", recipientErr)
	}
	wantTotal := new(big.Int).Mul(amount, big.NewInt(2))
	if recipientFinal.BalanceNHB.Cmp(wantTotal) != 0 {
		t.Fatalf("expected recipient credited %s total across both withdrawals, got %s", wantTotal, recipientFinal.BalanceNHB)
	}
}

// TestLendingFeeWithdraw_IntentMismatchRejected proves the senderless-tx gap
// the generic intent registry alone would leave open: since
// types.RequiresSignature is false for both withdrawal tx types, nothing
// stops a relayer from submitting a correctly, legitimately-signed Data
// payload under a stripped (or substituted) top-level IntentRef, which would
// otherwise skip core/state_transition.go's IntentRegistryValidate/Consume
// entirely (it only runs when len(tx.IntentRef) > 0) and let the withdrawal
// execute with no replay protection at all. Both apply functions must reject
// this before touching any state.
func TestLendingFeeWithdraw_IntentMismatchRejected(t *testing.T) {
	node := newTestNode(t)

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	adminAddr := toAddress(adminKey)
	assignRole(t, node, RoleLendingProtocolAdmin, adminAddr)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipientAddr20 := toAddress(recipientKey)
	recipient := crypto.MustNewAddress(crypto.NHBPrefix, recipientAddr20[:])
	recipientStr := recipient.String()

	const poolID = "default"
	moduleAddr := node.LendingModuleAddress()
	amount := big.NewInt(100)

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.LendingPutMarket(poolID, &lending.Market{PoolID: poolID, TotalNHBSupplied: big.NewInt(500)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed market: %v", err)
	}
	if err := manager.LendingPutFeeAccrual(poolID, &lending.FeeAccrual{ProtocolFeesWei: big.NewInt(150), DeveloperFeesWei: big.NewInt(0)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed fee accrual: %v", err)
	}
	moduleAcc, err := manager.GetAccount(moduleAddr.Bytes())
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("load module account: %v", err)
	}
	moduleAcc.BalanceNHB = big.NewInt(400)
	if err := manager.PutAccount(moduleAddr.Bytes(), moduleAcc); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("fund module account: %v", err)
	}
	node.stateMu.Unlock()

	nonce := "withdraw-1"
	signature, err := ethcrypto.Sign(LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce), adminKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal: %v", err)
	}
	payload, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, nonce, signature)
	if err != nil {
		t.Fatalf("encode withdrawal: %v", err)
	}

	// Deliberately omit IntentRef, as a relayer stripping it (or simply never
	// setting it) would.
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLendingWithdrawProtocolFees,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}

	node.stateMu.Lock()
	applyErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if !errors.Is(applyErr, ErrLendingFeeWithdrawIntentMismatch) {
		t.Fatalf("SECURITY REGRESSION: expected ErrLendingFeeWithdrawIntentMismatch for a missing IntentRef, got %v", applyErr)
	}

	node.stateMu.Lock()
	manager2 := nhbstate.NewManager(node.state.Trie)
	fees, ok, feesErr := manager2.LendingGetFeeAccrual(poolID)
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual: ok=%v err=%v", ok, feesErr)
	}
	if fees.ProtocolFeesWei.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("expected protocol fees unchanged at 150, got %s", fees.ProtocolFeesWei)
	}
}
