package core

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func TestStateProcessorPersistsLendingPoolState(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	tr, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := NewStateProcessor(tr)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	sp.SetLendingRiskParameters(lending.RiskParameters{
		MaxLTV:               7_500,
		LiquidationThreshold: 8_000,
	})
	sp.SetLendingAccrualConfig(0, 0, lending.DefaultInterestModel)

	supplierKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate supplier key: %v", err)
	}
	borrowerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate borrower key: %v", err)
	}

	if err := sp.setAccount(supplierKey.PubKey().Address().Bytes(), &types.Account{
		Nonce:       0,
		BalanceNHB:  mustBigInt(t, "10000000000000000000000"),
		BalanceZNHB: big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed supplier account: %v", err)
	}
	if err := sp.setAccount(borrowerKey.PubKey().Address().Bytes(), &types.Account{
		Nonce:       0,
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: mustBigInt(t, "50000000000000000000000"),
	}); err != nil {
		t.Fatalf("seed borrower account: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit seeded state: %v", err)
	}

	txs := []*types.Transaction{
		mustSignLendingTx(t, supplierKey, types.TxTypeLendingSupplyNHB, 0, mustBigInt(t, "1500000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, borrowerKey, types.TxTypeLendingDepositZNHB, 0, mustBigInt(t, "50000000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, borrowerKey, types.TxTypeLendingBorrowNHB, 1, mustBigInt(t, "250000000000000000000"), lendingNativePayload{PoolID: "default"}),
	}

	for idx, tx := range txs {
		height := uint64(idx + 1)
		sp.BeginBlock(height, time.Unix(int64(height), 0).UTC())
		if err := sp.ApplyTransaction(tx); err != nil {
			sp.EndBlock()
			t.Fatalf("apply tx %d: %v", idx, err)
		}
		sp.EndBlock()
		if _, err := sp.Commit(height); err != nil {
			t.Fatalf("commit tx %d: %v", idx, err)
		}
	}

	reloadedTrie, err := statetrie.NewTrie(db, sp.CurrentRoot().Bytes())
	if err != nil {
		t.Fatalf("reload trie: %v", err)
	}
	reloaded, err := NewStateProcessor(reloadedTrie)
	if err != nil {
		t.Fatalf("reload state processor: %v", err)
	}
	manager := nhbstate.NewManager(reloaded.Trie)

	market, ok, err := manager.LendingGetMarket("default")
	if err != nil {
		t.Fatalf("get market: %v", err)
	}
	if !ok || market == nil {
		t.Fatalf("expected default market to persist")
	}
	if market.TotalNHBSupplied == nil || market.TotalNHBSupplied.Cmp(mustBigInt(t, "1500000000000000000000")) != 0 {
		t.Fatalf("unexpected supplied NHB total: %v", market.TotalNHBSupplied)
	}
	if market.TotalNHBBorrowed == nil || market.TotalNHBBorrowed.Cmp(mustBigInt(t, "250000000000000000000")) != 0 {
		t.Fatalf("unexpected borrowed NHB total: %v", market.TotalNHBBorrowed)
	}

	var borrowerAddr [20]byte
	copy(borrowerAddr[:], borrowerKey.PubKey().Address().Bytes())
	userAccount, ok, err := manager.LendingGetUserAccount("default", borrowerAddr)
	if err != nil {
		t.Fatalf("get user account: %v", err)
	}
	if !ok || userAccount == nil {
		t.Fatalf("expected borrower lending account to persist")
	}
	if userAccount.CollateralZNHB == nil || userAccount.CollateralZNHB.Cmp(mustBigInt(t, "50000000000000000000000")) != 0 {
		t.Fatalf("unexpected collateral amount: %v", userAccount.CollateralZNHB)
	}
	if userAccount.DebtNHB == nil || userAccount.DebtNHB.Sign() <= 0 {
		t.Fatalf("expected borrower debt to persist, got %v", userAccount.DebtNHB)
	}

	borrowerBalance, err := manager.GetAccount(borrowerKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("get borrower balance account: %v", err)
	}
	if borrowerBalance.BalanceNHB == nil || borrowerBalance.BalanceNHB.Cmp(mustBigInt(t, "250000000000000000000")) != 0 {
		t.Fatalf("unexpected borrower NHB balance after borrow: %v", borrowerBalance.BalanceNHB)
	}
}

func TestLendingStateAdapterMigratesLegacyAccountState(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	tr, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := NewStateProcessor(tr)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	sp.SetLendingRiskParameters(lending.RiskParameters{
		MaxLTV:               7_500,
		LiquidationThreshold: 8_000,
	})

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	manager := nhbstate.NewManager(sp.Trie)
	account := &types.Account{
		Nonce:             0,
		BalanceNHB:        big.NewInt(0),
		BalanceZNHB:       big.NewInt(0),
		CollateralBalance: mustBigInt(t, "15000000000000000000000"),
		SupplyShares:      mustBigInt(t, "1500000000000000000000"),
		DebtPrincipal:     mustBigInt(t, "250000000000000000000"),
	}
	if err := manager.PutAccount(userKey.PubKey().Address().Bytes(), account); err != nil {
		t.Fatalf("seed legacy account: %v", err)
	}

	adapter := sp.lendingStateAdapter("default")
	user, err := adapter.GetUserAccount("default", crypto.MustNewAddress(crypto.NHBPrefix, userKey.PubKey().Address().Bytes()))
	if err != nil {
		t.Fatalf("migrate legacy user account: %v", err)
	}
	if user == nil {
		t.Fatalf("expected migrated lending account")
	}
	if user.CollateralZNHB.Cmp(account.CollateralBalance) != 0 {
		t.Fatalf("unexpected collateral after migration: %v", user.CollateralZNHB)
	}
	if user.DebtNHB.Cmp(account.DebtPrincipal) != 0 {
		t.Fatalf("unexpected debt after migration: %v", user.DebtNHB)
	}

	var raw [20]byte
	copy(raw[:], userKey.PubKey().Address().Bytes())
	storedUser, ok, err := manager.LendingGetUserAccount("default", raw)
	if err != nil {
		t.Fatalf("load migrated lending user: %v", err)
	}
	if !ok || storedUser == nil {
		t.Fatalf("expected migrated lending user to persist")
	}
	market, ok, err := manager.LendingGetMarket("default")
	if err != nil {
		t.Fatalf("load migrated market: %v", err)
	}
	if !ok || market == nil {
		t.Fatalf("expected migrated market to persist")
	}
	if market.TotalNHBSupplied == nil || market.TotalNHBSupplied.Sign() <= 0 {
		t.Fatalf("expected migrated supplied NHB total, got %v", market.TotalNHBSupplied)
	}
	if market.TotalNHBBorrowed == nil || market.TotalNHBBorrowed.Cmp(account.DebtPrincipal) != 0 {
		t.Fatalf("unexpected migrated borrowed total: %v", market.TotalNHBBorrowed)
	}
}

// seedUnhealthyBorrowerPosition sets up a supplier and a borrower who opens a
// position that's healthy at origination, then tightens the liquidation
// threshold to simulate a subsequent risk-parameter change (or, in the real
// world, collateral price decline / interest accrual) that leaves the
// borrower's existing position undercollateralized. Returns the processor
// and the three keys involved.
func seedUnhealthyBorrowerPosition(t *testing.T) (sp *StateProcessor, db storage.Database, supplierKey, borrowerKey, liquidatorKey *crypto.PrivateKey) {
	t.Helper()
	db = storage.NewMemDB()

	tr, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err = NewStateProcessor(tr)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	sp.SetLendingRiskParameters(lending.RiskParameters{
		MaxLTV:               8_000,
		LiquidationThreshold: 9_000,
		LiquidationBonus:     1_000,
	})
	// Zero-rate model: this test asserts exact post-liquidation balances, so
	// interest accrual (which DefaultInterestModel would apply) is
	// deliberately excluded -- it's covered by dedicated interest tests
	// elsewhere, not this one.
	sp.SetLendingAccrualConfig(0, 0, lending.NewInterestModel(0, 0, 0, 0.8))

	supplierKey, err = crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate supplier key: %v", err)
	}
	borrowerKey, err = crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate borrower key: %v", err)
	}
	liquidatorKey, err = crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate liquidator key: %v", err)
	}

	if err := sp.setAccount(supplierKey.PubKey().Address().Bytes(), &types.Account{
		BalanceNHB:  mustBigInt(t, "10000000000000000000000"),
		BalanceZNHB: big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed supplier account: %v", err)
	}
	if err := sp.setAccount(borrowerKey.PubKey().Address().Bytes(), &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: mustBigInt(t, "1000000000000000000000"),
	}); err != nil {
		t.Fatalf("seed borrower account: %v", err)
	}
	if err := sp.setAccount(liquidatorKey.PubKey().Address().Bytes(), &types.Account{
		BalanceNHB:  mustBigInt(t, "1000000000000000000000"),
		BalanceZNHB: big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed liquidator account: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit seeded state: %v", err)
	}

	txs := []*types.Transaction{
		mustSignLendingTx(t, supplierKey, types.TxTypeLendingSupplyNHB, 0, mustBigInt(t, "1000000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, borrowerKey, types.TxTypeLendingDepositZNHB, 0, mustBigInt(t, "1000000000000000000000"), lendingNativePayload{PoolID: "default"}),
		// 800 NHB borrowed against 1000 ZNHB collateral is 80% LTV -- allowed
		// under the 90% LiquidationThreshold configured above.
		mustSignLendingTx(t, borrowerKey, types.TxTypeLendingBorrowNHB, 1, mustBigInt(t, "800000000000000000000"), lendingNativePayload{PoolID: "default"}),
	}
	for idx, tx := range txs {
		height := uint64(idx + 1)
		sp.BeginBlock(height, time.Unix(int64(height), 0).UTC())
		if err := sp.ApplyTransaction(tx); err != nil {
			sp.EndBlock()
			t.Fatalf("apply seed tx %d: %v", idx, err)
		}
		sp.EndBlock()
		if _, err := sp.Commit(height); err != nil {
			t.Fatalf("commit seed tx %d: %v", idx, err)
		}
	}

	// Tighten the threshold below the borrower's now-existing 80% LTV so the
	// position is unhealthy going forward, without touching MaxLTV (which
	// only gates new borrows, not existing ones).
	sp.SetLendingRiskParameters(lending.RiskParameters{
		MaxLTV:               8_000,
		LiquidationThreshold: 7_000,
		LiquidationBonus:     1_000,
	})
	return sp, db, supplierKey, borrowerKey, liquidatorKey
}

func mustSignLendingLiquidateTx(t *testing.T, key *crypto.PrivateKey, nonce uint64, borrower crypto.Address) *types.Transaction {
	t.Helper()
	data, err := json.Marshal(lendingLiquidatePayload{PoolID: "default", Borrower: borrower.String()})
	if err != nil {
		t.Fatalf("marshal liquidate payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLendingLiquidate,
		Nonce:    nonce,
		To:       make([]byte, 20),
		Value:    big.NewInt(0),
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign liquidate tx: %v", err)
	}
	return tx
}

func TestApplyLendingLiquidate_SeizesCollateralAndClearsDebt(t *testing.T) {
	sp, db, _, borrowerKey, liquidatorKey := seedUnhealthyBorrowerPosition(t)
	defer db.Close()

	borrowerAddr := crypto.MustNewAddress(crypto.NHBPrefix, borrowerKey.PubKey().Address().Bytes())
	tx := mustSignLendingLiquidateTx(t, liquidatorKey, 0, borrowerAddr)

	sp.BeginBlock(4, time.Unix(4, 0).UTC())
	if err := sp.ApplyTransaction(tx); err != nil {
		sp.EndBlock()
		t.Fatalf("apply liquidate tx: %v", err)
	}
	sp.EndBlock()
	if _, err := sp.Commit(4); err != nil {
		t.Fatalf("commit liquidate tx: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	var borrowerRaw [20]byte
	copy(borrowerRaw[:], borrowerKey.PubKey().Address().Bytes())
	userAccount, ok, err := manager.LendingGetUserAccount("default", borrowerRaw)
	if err != nil {
		t.Fatalf("get borrower lending account: %v", err)
	}
	if !ok || userAccount == nil {
		t.Fatalf("expected borrower lending account to persist")
	}
	if userAccount.DebtNHB == nil || userAccount.DebtNHB.Sign() != 0 {
		t.Fatalf("expected borrower debt cleared, got %v", userAccount.DebtNHB)
	}
	// 800 NHB repaid seizes 800 * 1.10 = 880 ZNHB of the borrower's 1000 ZNHB collateral.
	if userAccount.CollateralZNHB == nil || userAccount.CollateralZNHB.Cmp(mustBigInt(t, "120000000000000000000")) != 0 {
		t.Fatalf("unexpected remaining borrower collateral: %v", userAccount.CollateralZNHB)
	}

	market, ok, err := manager.LendingGetMarket("default")
	if err != nil {
		t.Fatalf("get market: %v", err)
	}
	if !ok || market == nil || market.TotalNHBBorrowed == nil || market.TotalNHBBorrowed.Sign() != 0 {
		t.Fatalf("expected market borrowed total to reset to zero, got %v", market.TotalNHBBorrowed)
	}

	liquidatorAcc, err := manager.GetAccount(liquidatorKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("get liquidator account: %v", err)
	}
	if liquidatorAcc.BalanceNHB == nil || liquidatorAcc.BalanceNHB.Cmp(mustBigInt(t, "200000000000000000000")) != 0 {
		t.Fatalf("unexpected liquidator NHB balance after repay: %v", liquidatorAcc.BalanceNHB)
	}
	// No custom collateral routing configured -- the liquidator receives the
	// full seized amount.
	if liquidatorAcc.BalanceZNHB == nil || liquidatorAcc.BalanceZNHB.Cmp(mustBigInt(t, "880000000000000000000")) != 0 {
		t.Fatalf("unexpected liquidator ZNHB balance after seizure: %v", liquidatorAcc.BalanceZNHB)
	}
	if liquidatorAcc.Nonce != 1 {
		t.Fatalf("expected liquidator nonce incremented, got %d", liquidatorAcc.Nonce)
	}
}

func TestApplyLendingLiquidate_RejectsHealthyPosition(t *testing.T) {
	sp, db, _, borrowerKey, liquidatorKey := seedUnhealthyBorrowerPosition(t)
	defer db.Close()

	// Restore a generous threshold so the borrower's 80% LTV position is
	// healthy again -- liquidation must be rejected.
	sp.SetLendingRiskParameters(lending.RiskParameters{
		MaxLTV:               8_000,
		LiquidationThreshold: 9_000,
		LiquidationBonus:     1_000,
	})

	borrowerAddr := crypto.MustNewAddress(crypto.NHBPrefix, borrowerKey.PubKey().Address().Bytes())
	tx := mustSignLendingLiquidateTx(t, liquidatorKey, 0, borrowerAddr)

	sp.BeginBlock(4, time.Unix(4, 0).UTC())
	err := sp.ApplyTransaction(tx)
	sp.EndBlock()
	if err == nil {
		t.Fatalf("expected liquidation of a healthy position to be rejected")
	}
}

func TestApplyLendingLiquidate_RejectsSelfLiquidation(t *testing.T) {
	sp, db, _, borrowerKey, _ := seedUnhealthyBorrowerPosition(t)
	defer db.Close()

	if err := sp.setAccount(borrowerKey.PubKey().Address().Bytes(), &types.Account{
		BalanceNHB:  mustBigInt(t, "1000000000000000000000"),
		BalanceZNHB: big.NewInt(0),
	}); err != nil {
		t.Fatalf("top up borrower NHB: %v", err)
	}

	borrowerAddr := crypto.MustNewAddress(crypto.NHBPrefix, borrowerKey.PubKey().Address().Bytes())
	tx := mustSignLendingLiquidateTx(t, borrowerKey, 2, borrowerAddr)

	sp.BeginBlock(4, time.Unix(4, 0).UTC())
	err := sp.ApplyTransaction(tx)
	sp.EndBlock()
	if err == nil {
		t.Fatalf("expected self-liquidation to be rejected")
	}
}

func mustSignLendingTx(t *testing.T, key *crypto.PrivateKey, txType types.TxType, nonce uint64, value *big.Int, payload lendingNativePayload) *types.Transaction {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal lending payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    nonce,
		To:       make([]byte, 20),
		Value:    new(big.Int).Set(value),
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign lending tx: %v", err)
	}
	return tx
}

func mustBigInt(t *testing.T, value string) *big.Int {
	t.Helper()
	out, ok := new(big.Int).SetString(value, 10)
	if !ok {
		t.Fatalf("invalid big.Int literal %q", value)
	}
	return out
}
