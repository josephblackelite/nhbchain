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
	intentExpiry := uint64(time.Now().Add(time.Hour).Unix())
	intentRef := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce, intentExpiry)
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
		IntentExpiry: intentExpiry,
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
	intentExpiry := uint64(time.Now().Add(time.Hour).Unix())
	intentRef := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce, intentExpiry)
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
		IntentExpiry: intentExpiry,
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
	intentExpiry := uint64(time.Now().Add(time.Hour).Unix())
	intentRef := LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce, intentExpiry)
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
		IntentExpiry: intentExpiry,
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
	intentExpiry := uint64(time.Now().Add(time.Hour).Unix())
	intentRef := LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce, intentExpiry)
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
		IntentExpiry: intentExpiry,
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
	intentExpiry1 := uint64(time.Now().Add(time.Hour).Unix())
	intentRef1 := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce1, intentExpiry1)
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
		IntentExpiry: intentExpiry1,
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
	intentExpiry2 := uint64(time.Now().Add(time.Hour).Unix())
	intentRef2 := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce2, intentExpiry2)
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
		IntentExpiry: intentExpiry2,
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
	intentExpiry1 := uint64(time.Now().Add(time.Hour).Unix())
	intentRef1 := LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce1, intentExpiry1)
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
		IntentExpiry: intentExpiry1,
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
	intentExpiry2 := uint64(time.Now().Add(time.Hour).Unix())
	intentRef2 := LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce2, intentExpiry2)
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
		IntentExpiry: intentExpiry2,
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
	// Deliberately signed for IntentExpiry 0, matching the zero-value tx
	// below's unset IntentExpiry -- irrelevant to this test, which checks
	// that a missing tx.IntentRef is rejected regardless of what the
	// signature covers.
	signature, err := ethcrypto.Sign(LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce, 0), adminKey.PrivateKey)
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

// TestLendingWithdrawProtocolFeesApply_RejectsTTLExpiryReplayWithTamperedIntentExpiry
// is the direct regression test for the second-stage NHB-AUDIT TTL-replay
// finding: two independent adversarial reviews PROVED (each with an executed
// PoC) that binding tx.IntentRef to a signed hash of
// poolId|recipient|amountWei|nonce alone only narrowed the earlier replay
// window instead of closing it, because tx.IntentExpiry itself stayed
// unsigned and unchecked against anything the admin signed. These tx types
// are senderless, so nothing covered tx.IntentExpiry cryptographically.
// core/state/intent_registry.go's IntentRegistryValidate clamps every
// record's *stored* expiry to min(requestedExpiry, now+defaultIntentTTL),
// and once that stored expiry lapses, the next validate call DELETES the
// "consumed" record (returning ErrIntentExpired) as a side effect instead of
// remembering the ref was ever used. Combining these: once a withdrawal's
// own declared expiry elapsed, a relayer could replay the original,
// still-publicly-visible signed Data payload by submitting it twice with a
// freshly chosen, future tx.IntentExpiry -- the first resubmission failed
// with "intent: expired" but purged the stale consumed record as a side
// effect, and an immediate second resubmission (same IntentRef, same signed
// Data, another future IntentExpiry) succeeded, re-running the withdrawal
// using the original admin signature.
//
// This test proves the fix closes the gap rather than just moving it again:
//  1. Replaying the exact same signed Data payload (same IntentRef, same
//     signature) under a NEW, self-chosen, future IntentExpiry reproduces
//     the verifiers' exact two steps: the first resubmission still fails
//     with ErrIntentExpired and still purges the stale registry record,
//     exactly as before this fix (core/state_transition.go's generic
//     IntentRegistryValidate runs on the raw tx.IntentRef/tx.IntentExpiry
//     before this tx type's own apply function is ever reached, so it
//     cannot by itself know the expiry is tampered). The critical
//     difference is the IMMEDIATE SECOND resubmission of that same tampered
//     transaction: before this fix it would validate cleanly (no stored
//     record left to collide with) and reach the apply function, which
//     recomputed a hash that never covered IntentExpiry, so the original
//     admin signature still verified and the withdrawal silently
//     re-executed. AFTER this fix, that second resubmission instead fails
//     with ErrLendingFeeWithdrawIntentMismatch: the apply function now
//     recomputes the signing hash using the tampered IntentExpiry, which no
//     longer equals tx.IntentRef (fixed forever by the admin's original
//     signature over the real, signed expiry).
//  2. Replaying the exact same, byte-identical transaction (same IntentRef,
//     same now-PAST IntentExpiry -- the only IntentExpiry any valid
//     signature for this withdrawal could ever carry) after its own signed
//     expiry has elapsed fails honestly and consistently with
//     ErrIntentExpired on every attempt -- never silently succeeding --
//     because IntentRegistryValidate's own
//     "unixNow >= requestedExpiry" check runs unconditionally, before any
//     stored-record lookup or deletion, and an attacker can no longer move
//     that submitted expiry forward without invalidating the signature
//     check in (1).
func TestLendingWithdrawProtocolFeesApply_RejectsTTLExpiryReplayWithTamperedIntentExpiry(t *testing.T) {
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

	start := time.Unix(1_700_000_000, 0).UTC()
	node.stateMu.Lock()
	node.state.nowFunc = func() time.Time { return start }
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

	// The admin signs ONE withdrawal, with IntentExpiry comfortably inside
	// the default 24h registry TTL, so the registry's own TTL clamp never
	// shortens the stored expiry below what was actually signed -- isolating
	// the exact property under test (what happens once THIS IntentExpiry
	// itself elapses).
	nonce := "withdraw-1"
	signedExpiry := uint64(start.Add(time.Hour).Unix())
	intentRef := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce, signedExpiry)
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
		IntentExpiry: signedExpiry,
	}

	node.stateMu.Lock()
	err = node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("apply original withdrawal: %v", err)
	}

	// Advance past the signed IntentExpiry and let the pool re-accrue --
	// exactly the precondition both verifiers' PoCs relied on.
	afterExpiry := start.Add(2 * time.Hour)
	node.stateMu.Lock()
	node.state.nowFunc = func() time.Time { return afterExpiry }
	node.stateMu.Unlock()
	seedFees(100)

	// Replay the EXACT SAME signed Data payload (same IntentRef, same
	// signature) under a NEW, self-chosen, future IntentExpiry -- the
	// verifiers' exact two-step exploit:
	//
	//  1. The first resubmission fails with ErrIntentExpired, exactly as it
	//     did before this fix -- core/state_transition.go's generic
	//     IntentRegistryValidate runs BEFORE this tx type's own apply
	//     function ever sees it, operating on the raw submitted
	//     tx.IntentRef/tx.IntentExpiry alone. It finds the existing record
	//     (stored under the original, now-lapsed signed expiry) and purges
	//     it as a side effect, reporting ErrIntentExpired.
	//  2. BEFORE the fix, an immediate second, identical resubmission would
	//     then find no stored record, validate cleanly against the
	//     attacker's future tampered expiry, and reach the apply function,
	//     which (pre-fix) recomputed a signing hash that never covered
	//     IntentExpiry at all -- so tx.IntentRef still equaled it, the
	//     original admin signature still verified, and the withdrawal
	//     silently re-executed. AFTER the fix, this second resubmission must
	//     instead fail with ErrLendingFeeWithdrawIntentMismatch: the apply
	//     function now recomputes the hash using the tampered IntentExpiry,
	//     which no longer equals tx.IntentRef (fixed forever by the admin's
	//     original signature over the real, signed expiry).
	tamperedExpiry := uint64(afterExpiry.Add(time.Hour).Unix())
	tamperedTx := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeLendingWithdrawProtocolFees,
		Data:         payload,
		GasLimit:     0,
		GasPrice:     big.NewInt(0),
		IntentRef:    intentRef,
		IntentExpiry: tamperedExpiry,
	}
	node.stateMu.Lock()
	firstTamperedErr := node.state.ApplyTransaction(tamperedTx)
	node.stateMu.Unlock()
	if !errors.Is(firstTamperedErr, nhbstate.ErrIntentExpired) {
		t.Fatalf("test setup: expected the first tampered resubmission to purge the stale record with ErrIntentExpired, got %v", firstTamperedErr)
	}

	node.stateMu.Lock()
	secondTamperedErr := node.state.ApplyTransaction(tamperedTx)
	node.stateMu.Unlock()
	if !errors.Is(secondTamperedErr, ErrLendingFeeWithdrawIntentMismatch) {
		t.Fatalf("SECURITY REGRESSION: expected ErrLendingFeeWithdrawIntentMismatch on the immediate second tampered-IntentExpiry resubmission, got %v", secondTamperedErr)
	}

	node.stateMu.Lock()
	managerAfterTamper := nhbstate.NewManager(node.state.Trie)
	feesAfterTamper, ok, feesErr := managerAfterTamper.LendingGetFeeAccrual(poolID)
	recipientAfterTamper, recipientErr := managerAfterTamper.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual after tampered replay: ok=%v err=%v", ok, feesErr)
	}
	if feesAfterTamper.ProtocolFeesWei.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("SECURITY REGRESSION: tampered replay must not touch fee accrual, got %s", feesAfterTamper.ProtocolFeesWei)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account after tampered replay: %v", recipientErr)
	}
	if recipientAfterTamper.BalanceNHB.Cmp(amount) != 0 {
		t.Fatalf("SECURITY REGRESSION: tampered replay must not credit the recipient again, got %s", recipientAfterTamper.BalanceNHB)
	}

	// Finally, replay the EXACT SAME, byte-identical transaction (same
	// IntentRef, same now-PAST IntentExpiry the admin actually signed) twice
	// in a row. Must fail honestly and consistently with ErrIntentExpired --
	// never silently succeed, and never exhibit the old
	// delete-then-immediate-retry window -- confirming that once THIS
	// transaction's own signed IntentExpiry has itself passed, the exact
	// same signed bytes can never be resubmitted and succeed.
	for attempt := 1; attempt <= 2; attempt++ {
		node.stateMu.Lock()
		sameBytesErr := node.state.ApplyTransaction(tx)
		node.stateMu.Unlock()
		if !errors.Is(sameBytesErr, nhbstate.ErrIntentExpired) {
			t.Fatalf("SECURITY REGRESSION: expected ErrIntentExpired replaying the original now-expired withdrawal (attempt %d), got %v", attempt, sameBytesErr)
		}
	}

	node.stateMu.Lock()
	managerFinal := nhbstate.NewManager(node.state.Trie)
	feesFinal, ok, feesErr := managerFinal.LendingGetFeeAccrual(poolID)
	recipientFinal, recipientErr := managerFinal.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual after final replay attempts: ok=%v err=%v", ok, feesErr)
	}
	if feesFinal.ProtocolFeesWei.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("SECURITY REGRESSION: final replay attempts must not touch fee accrual, got %s", feesFinal.ProtocolFeesWei)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account after final replay attempts: %v", recipientErr)
	}
	if recipientFinal.BalanceNHB.Cmp(amount) != 0 {
		t.Fatalf("SECURITY REGRESSION: final replay attempts must not credit the recipient again, got %s", recipientFinal.BalanceNHB)
	}
}

// TestLendingWithdrawDeveloperFeesApply_RejectsTTLExpiryReplayWithTamperedIntentExpiry
// is
// TestLendingWithdrawProtocolFeesApply_RejectsTTLExpiryReplayWithTamperedIntentExpiry's
// counterpart for TxTypeLendingWithdrawDeveloperFees -- both verifiers named
// LendingDeveloperFeeWithdrawSigningHash explicitly alongside
// LendingProtocolFeeWithdrawSigningHash as sharing the identical unsigned-
// IntentExpiry defect, so both tx types need the identical regression
// coverage.
func TestLendingWithdrawDeveloperFeesApply_RejectsTTLExpiryReplayWithTamperedIntentExpiry(t *testing.T) {
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

	start := time.Unix(1_700_000_000, 0).UTC()
	node.stateMu.Lock()
	node.state.nowFunc = func() time.Time { return start }
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

	nonce := "withdraw-1"
	signedExpiry := uint64(start.Add(time.Hour).Unix())
	intentRef := LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce, signedExpiry)
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
		IntentExpiry: signedExpiry,
	}

	node.stateMu.Lock()
	err = node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("apply original withdrawal: %v", err)
	}

	afterExpiry := start.Add(2 * time.Hour)
	node.stateMu.Lock()
	node.state.nowFunc = func() time.Time { return afterExpiry }
	node.stateMu.Unlock()
	seedFees(100)

	tamperedExpiry := uint64(afterExpiry.Add(time.Hour).Unix())
	tamperedTx := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeLendingWithdrawDeveloperFees,
		Data:         payload,
		GasLimit:     0,
		GasPrice:     big.NewInt(0),
		IntentRef:    intentRef,
		IntentExpiry: tamperedExpiry,
	}
	node.stateMu.Lock()
	firstTamperedErr := node.state.ApplyTransaction(tamperedTx)
	node.stateMu.Unlock()
	if !errors.Is(firstTamperedErr, nhbstate.ErrIntentExpired) {
		t.Fatalf("test setup: expected the first tampered resubmission to purge the stale record with ErrIntentExpired, got %v", firstTamperedErr)
	}

	node.stateMu.Lock()
	secondTamperedErr := node.state.ApplyTransaction(tamperedTx)
	node.stateMu.Unlock()
	if !errors.Is(secondTamperedErr, ErrLendingFeeWithdrawIntentMismatch) {
		t.Fatalf("SECURITY REGRESSION: expected ErrLendingFeeWithdrawIntentMismatch on the immediate second tampered-IntentExpiry resubmission, got %v", secondTamperedErr)
	}

	node.stateMu.Lock()
	managerAfterTamper := nhbstate.NewManager(node.state.Trie)
	feesAfterTamper, ok, feesErr := managerAfterTamper.LendingGetFeeAccrual(poolID)
	recipientAfterTamper, recipientErr := managerAfterTamper.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual after tampered replay: ok=%v err=%v", ok, feesErr)
	}
	if feesAfterTamper.DeveloperFeesWei.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("SECURITY REGRESSION: tampered replay must not touch fee accrual, got %s", feesAfterTamper.DeveloperFeesWei)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account after tampered replay: %v", recipientErr)
	}
	if recipientAfterTamper.BalanceNHB.Cmp(amount) != 0 {
		t.Fatalf("SECURITY REGRESSION: tampered replay must not credit the recipient again, got %s", recipientAfterTamper.BalanceNHB)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		node.stateMu.Lock()
		sameBytesErr := node.state.ApplyTransaction(tx)
		node.stateMu.Unlock()
		if !errors.Is(sameBytesErr, nhbstate.ErrIntentExpired) {
			t.Fatalf("SECURITY REGRESSION: expected ErrIntentExpired replaying the original now-expired withdrawal (attempt %d), got %v", attempt, sameBytesErr)
		}
	}

	node.stateMu.Lock()
	managerFinal := nhbstate.NewManager(node.state.Trie)
	feesFinal, ok, feesErr := managerFinal.LendingGetFeeAccrual(poolID)
	recipientFinal, recipientErr := managerFinal.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual after final replay attempts: ok=%v err=%v", ok, feesErr)
	}
	if feesFinal.DeveloperFeesWei.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("SECURITY REGRESSION: final replay attempts must not touch fee accrual, got %s", feesFinal.DeveloperFeesWei)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account after final replay attempts: %v", recipientErr)
	}
	if recipientFinal.BalanceNHB.Cmp(amount) != 0 {
		t.Fatalf("SECURITY REGRESSION: final replay attempts must not credit the recipient again, got %s", recipientFinal.BalanceNHB)
	}
}

// TestLendingWithdrawProtocolFeesApply_RejectsExpiryBeyondRegistryTTL is the
// direct, end-to-end regression test for the third-stage NHB-AUDIT
// TTL-clamp-gap finding: a re-verifier PROVED LIVE that binding
// tx.IntentExpiry into the signed hash (closing the tampered-expiry replay)
// still left a gap whenever the admin's own, honestly SIGNED IntentExpiry
// simply reaches further into the future than
// core/state/intent_registry.go's IntentRegistryValidate will ever actually
// retain the stored record for. Reproduces the PoC exactly:
//
//   - The registry's effective TTL for this submission is clamped to 1h
//     (via node.state.intentTTL), while the admin signs IntentExpiry =
//     now+10h -- a perfectly validly signed, byte-identical transaction the
//     admin never tampered with.
//   - Before this fix: the first submission would succeed but the STORED
//     record would be silently clamped to now+1h; after advancing to
//     now+2h (nine hours before the real signed deadline, but past the
//     clamp), a first resubmission would purge the stale record with
//     ErrIntentExpired and an immediate second, byte-identical resubmission
//     would validate cleanly and re-execute the withdrawal -- doubling the
//     recipient's balance with zero tampering anywhere.
//   - After this fix: applyLendingWithdrawProtocolFeesTransaction rejects
//     the transaction with ErrLendingFeeWithdrawExpiryTooFar before ANY
//     state is touched -- not just on the replay, but on the very first
//     submission, since a signed expiry that outlives the registry's
//     retention window can never be honored in full. The transaction never
//     enters the registry, never debits the fee accrual, and never credits
//     the recipient, no matter how many times it is resubmitted or how far
//     time advances.
func TestLendingWithdrawProtocolFeesApply_RejectsExpiryBeyondRegistryTTL(t *testing.T) {
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

	start := time.Unix(1_700_000_000, 0).UTC()
	node.stateMu.Lock()
	node.state.nowFunc = func() time.Time { return start }
	// The re-verifier's exact precondition: the registry's effective TTL for
	// this submission is 1h, far shorter than the 10h the admin is about to
	// sign.
	node.state.intentTTL = time.Hour
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.LendingPutMarket(poolID, &lending.Market{PoolID: poolID, TotalNHBSupplied: big.NewInt(1000)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed market: %v", err)
	}
	if err := manager.LendingPutFeeAccrual(poolID, &lending.FeeAccrual{ProtocolFeesWei: big.NewInt(100), DeveloperFeesWei: big.NewInt(0)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed fee accrual: %v", err)
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

	// The admin signs a withdrawal with IntentExpiry = now+10h -- ten times
	// the registry's 1h effective TTL. This signature is entirely legitimate
	// and the transaction bytes below are never tampered with at any point
	// in this test.
	nonce := "withdraw-1"
	signedExpiry := uint64(start.Add(10 * time.Hour).Unix())
	intentRef := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce, signedExpiry)
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
		IntentExpiry: signedExpiry,
	}

	assertUntouched := func(step string) {
		t.Helper()
		node.stateMu.Lock()
		m := nhbstate.NewManager(node.state.Trie)
		fees, ok, feesErr := m.LendingGetFeeAccrual(poolID)
		recipientAcc, recipientErr := m.GetAccount(recipient.Bytes())
		node.stateMu.Unlock()
		if feesErr != nil || !ok {
			t.Fatalf("[%s] read fee accrual: ok=%v err=%v", step, ok, feesErr)
		}
		if fees.ProtocolFeesWei.Cmp(big.NewInt(100)) != 0 {
			t.Fatalf("SECURITY REGRESSION [%s]: protocol fees must remain untouched at 100, got %s", step, fees.ProtocolFeesWei)
		}
		if recipientErr != nil {
			t.Fatalf("[%s] read recipient account: %v", step, recipientErr)
		}
		if recipientAcc.BalanceNHB.Sign() != 0 {
			t.Fatalf("SECURITY REGRESSION [%s]: recipient must never be credited, got %s", step, recipientAcc.BalanceNHB)
		}
	}

	// The very first submission must already be rejected -- unlike the
	// tampered-IntentExpiry fix, this gap does not require any replay at
	// all; a single, originally-signed transaction with an out-of-bound
	// expiry can never be honored in full by the registry.
	node.stateMu.Lock()
	firstErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if !errors.Is(firstErr, ErrLendingFeeWithdrawExpiryTooFar) {
		t.Fatalf("SECURITY REGRESSION: expected ErrLendingFeeWithdrawExpiryTooFar on the first submission, got %v", firstErr)
	}
	assertUntouched("first submission")

	// Resubmitting the exact same, byte-identical transaction immediately
	// must fail identically -- no window where it ever validates.
	node.stateMu.Lock()
	secondErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if !errors.Is(secondErr, ErrLendingFeeWithdrawExpiryTooFar) {
		t.Fatalf("SECURITY REGRESSION: expected ErrLendingFeeWithdrawExpiryTooFar on immediate resubmission, got %v", secondErr)
	}
	assertUntouched("immediate resubmission")

	// Finally, reproduce the re-verifier's exact timeline: advance to now+2h
	// (past the 1h TTL clamp, nine hours before the real signed deadline)
	// and resubmit the same unmodified transaction once more. Before this
	// fix this is precisely the step that would have validated cleanly
	// (after an initial purge) and re-executed the withdrawal; after this
	// fix it must still fail the exact same way, with zero state touched.
	afterTTLClamp := start.Add(2 * time.Hour)
	node.stateMu.Lock()
	node.state.nowFunc = func() time.Time { return afterTTLClamp }
	thirdErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if !errors.Is(thirdErr, ErrLendingFeeWithdrawExpiryTooFar) {
		t.Fatalf("SECURITY REGRESSION: expected ErrLendingFeeWithdrawExpiryTooFar after advancing past the TTL clamp, got %v", thirdErr)
	}
	assertUntouched("after advancing past the TTL clamp")
}

// TestLendingWithdrawDeveloperFeesApply_RejectsExpiryBeyondRegistryTTL is
// TestLendingWithdrawProtocolFeesApply_RejectsExpiryBeyondRegistryTTL's
// counterpart for TxTypeLendingWithdrawDeveloperFees -- the re-verifier
// named LendingDeveloperFeeWithdrawSigningHash explicitly alongside
// LendingProtocolFeeWithdrawSigningHash as sharing the identical TTL-clamp
// gap, so both tx types need the identical regression coverage.
func TestLendingWithdrawDeveloperFeesApply_RejectsExpiryBeyondRegistryTTL(t *testing.T) {
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

	start := time.Unix(1_700_000_000, 0).UTC()
	node.stateMu.Lock()
	node.state.nowFunc = func() time.Time { return start }
	node.state.intentTTL = time.Hour
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.LendingPutMarket(poolID, &lending.Market{PoolID: poolID, DeveloperOwner: developerOwner, TotalNHBSupplied: big.NewInt(1000)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed market: %v", err)
	}
	if err := manager.LendingPutFeeAccrual(poolID, &lending.FeeAccrual{ProtocolFeesWei: big.NewInt(0), DeveloperFeesWei: big.NewInt(100)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed fee accrual: %v", err)
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

	nonce := "withdraw-1"
	signedExpiry := uint64(start.Add(10 * time.Hour).Unix())
	intentRef := LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce, signedExpiry)
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
		IntentExpiry: signedExpiry,
	}

	node.stateMu.Lock()
	firstErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if !errors.Is(firstErr, ErrLendingFeeWithdrawExpiryTooFar) {
		t.Fatalf("SECURITY REGRESSION: expected ErrLendingFeeWithdrawExpiryTooFar on the first submission, got %v", firstErr)
	}

	afterTTLClamp := start.Add(2 * time.Hour)
	node.stateMu.Lock()
	node.state.nowFunc = func() time.Time { return afterTTLClamp }
	secondErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if !errors.Is(secondErr, ErrLendingFeeWithdrawExpiryTooFar) {
		t.Fatalf("SECURITY REGRESSION: expected ErrLendingFeeWithdrawExpiryTooFar after advancing past the TTL clamp, got %v", secondErr)
	}

	node.stateMu.Lock()
	m := nhbstate.NewManager(node.state.Trie)
	fees, ok, feesErr := m.LendingGetFeeAccrual(poolID)
	recipientAcc, recipientErr := m.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual: ok=%v err=%v", ok, feesErr)
	}
	if fees.DeveloperFeesWei.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("SECURITY REGRESSION: developer fees must remain untouched at 100, got %s", fees.DeveloperFeesWei)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account: %v", recipientErr)
	}
	if recipientAcc.BalanceNHB.Sign() != 0 {
		t.Fatalf("SECURITY REGRESSION: recipient must never be credited, got %s", recipientAcc.BalanceNHB)
	}
}

// TestLendingWithdrawProtocolFeesApply_AllowsExpiryWithinTightTTL confirms
// the fix above does not regress the ordinary case: a withdrawal signed
// comfortably within -- or exactly at -- a short registry TTL must still
// apply exactly as before, even when that TTL is far shorter than the
// default 24h (core/state_transition.go's defaultIntentTTL). This isolates
// the exact boundary the new check enforces (reject only when
// tx.IntentExpiry strictly exceeds now+ttl) from the TTL-exceeding case
// covered above.
func TestLendingWithdrawProtocolFeesApply_AllowsExpiryWithinTightTTL(t *testing.T) {
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

	start := time.Unix(1_700_000_000, 0).UTC()
	node.stateMu.Lock()
	node.state.nowFunc = func() time.Time { return start }
	node.state.intentTTL = time.Hour
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.LendingPutMarket(poolID, &lending.Market{PoolID: poolID, TotalNHBSupplied: big.NewInt(1000)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed market: %v", err)
	}
	if err := manager.LendingPutFeeAccrual(poolID, &lending.FeeAccrual{ProtocolFeesWei: big.NewInt(100), DeveloperFeesWei: big.NewInt(0)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed fee accrual: %v", err)
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

	// Signed for exactly now+1h -- precisely the registry's TTL limit, not
	// one second beyond it. The new check must allow this (it only rejects
	// when IntentExpiry is STRICTLY greater than now+ttl), matching
	// core/state/intent_registry.go's own clamp, which leaves a requested
	// expiry exactly at the limit unclamped.
	nonce := "withdraw-1"
	signedExpiry := uint64(start.Add(time.Hour).Unix())
	intentRef := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String(), nonce, signedExpiry)
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
		IntentExpiry: signedExpiry,
	}

	node.stateMu.Lock()
	applyErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if applyErr != nil {
		t.Fatalf("expected legitimate withdrawal signed exactly at the TTL boundary to succeed, got %v", applyErr)
	}

	node.stateMu.Lock()
	m := nhbstate.NewManager(node.state.Trie)
	fees, ok, feesErr := m.LendingGetFeeAccrual(poolID)
	recipientAcc, recipientErr := m.GetAccount(recipient.Bytes())
	node.stateMu.Unlock()
	if feesErr != nil || !ok {
		t.Fatalf("read fee accrual: ok=%v err=%v", ok, feesErr)
	}
	if fees.ProtocolFeesWei.Sign() != 0 {
		t.Fatalf("expected protocol fees drained to 0, got %s", fees.ProtocolFeesWei)
	}
	if recipientErr != nil {
		t.Fatalf("read recipient account: %v", recipientErr)
	}
	if recipientAcc.BalanceNHB.Cmp(amount) != 0 {
		t.Fatalf("expected recipient credited %s, got %s", amount, recipientAcc.BalanceNHB)
	}
}
