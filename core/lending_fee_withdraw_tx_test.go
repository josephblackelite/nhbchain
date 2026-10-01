package core

import (
	"errors"
	"math/big"
	"testing"

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
	signature, err := ethcrypto.Sign(LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String()), adminKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal: %v", err)
	}
	payload, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, signature)
	if err != nil {
		t.Fatalf("encode withdrawal: %v", err)
	}
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
	signature, err := ethcrypto.Sign(LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amount.String()), rogueKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal: %v", err)
	}
	payload, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, signature)
	if err != nil {
		t.Fatalf("encode withdrawal: %v", err)
	}
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
	signature, err := ethcrypto.Sign(LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String()), developerKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal: %v", err)
	}
	payload, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, signature)
	if err != nil {
		t.Fatalf("encode withdrawal: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLendingWithdrawDeveloperFees,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
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
	signature, err := ethcrypto.Sign(LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amount.String()), rogueKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign withdrawal: %v", err)
	}
	payload, err := encodeLendingFeeWithdrawTransaction(poolID, recipientStr, amount, signature)
	if err != nil {
		t.Fatalf("encode withdrawal: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLendingWithdrawDeveloperFees,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
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
