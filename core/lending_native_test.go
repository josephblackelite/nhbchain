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
