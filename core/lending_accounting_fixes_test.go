package core

import (
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

const lendingAccountingTestActivation = 100

// marketStampAfterFixedTermBorrow runs the whole transaction pipeline for one
// fixed-term borrow at the given height and returns the block time the pool's
// market is stamped with afterwards.
func marketStampAfterFixedTermBorrow(t *testing.T, gate, height uint64) uint64 {
	t.Helper()
	sp, a, _ := lendingTwoBorrowers(t, 0)
	sp.SetLendingAccountingFixesActivationHeight(gate)
	tx := mustSignLendingFixedTermBorrowTx(t, a, 1, mustBigInt(t, "100000000000000000000"), 30)
	borrowAt(t, sp, a, tx, height)
	market, ok, err := nhbstate.NewManager(sp.Trie).LendingGetMarket("default")
	if err != nil || !ok {
		t.Fatalf("load the pool's market: %v %v", ok, err)
	}
	return market.LastUpdateTimestamp
}

// The accrual a fixed-term borrow performs (PL-R5T-50) happens from the height
// and not before: the market's stamp is the only thing a borrow against an idle
// pool changes in the accrual, so it shows both sides of the gate.
func TestFixedTermBorrowAccruesFirstOnlyFromTheAccountingFixesHeight(t *testing.T) {
	// The setup transactions run in blocks 1..3 and leave the market stamped at 2 or 3.
	stampBefore := marketStampAfterFixedTermBorrow(t, 0, 10_000_000)
	if stampBefore > 3 {
		t.Fatalf("setup left the market stamped at %d, expected a value from the setup blocks", stampBefore)
	}
	cases := []struct {
		name    string
		gate    uint64
		height  uint64
		accrues bool
	}{
		{"DefaultOffNeverAccruesFirst", 0, 10_000_000, false},
		{"BelowActivationDoesNot", lendingAccountingTestActivation, lendingAccountingTestActivation - 1, false},
		{"AtActivationAccruesFirst", lendingAccountingTestActivation, lendingAccountingTestActivation, true},
		{"AboveActivationAccruesFirst", lendingAccountingTestActivation, lendingAccountingTestActivation + 40, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := marketStampAfterFixedTermBorrow(t, tc.gate, tc.height)
			if tc.accrues && got != tc.height {
				t.Fatalf("market stamped at %d after the borrow at height %d, want %d (accrued first)", got, tc.height, tc.height)
			}
			if !tc.accrues && got != stampBefore {
				t.Fatalf("market stamp moved to %d after the borrow at height %d, want it left at %d", got, tc.height, stampBefore)
			}
		})
	}
}

// The gate must survive Copy and a rebuild of the state processor from the node.
func TestLendingAccountingFixesGateReachesBlockState(t *testing.T) {
	node := newTestNode(t)
	node.stateMu.RLock()
	unset := node.state.lendingAccountingFixesFromHeight
	node.stateMu.RUnlock()
	if unset != 0 {
		t.Fatalf("a new node has activation height %d, want 0 (off)", unset)
	}

	node.SetLendingAccountingFixesActivationHeight(lendingAccountingTestActivation)

	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	if got := node.state.lendingAccountingFixesFromHeight; got != lendingAccountingTestActivation {
		t.Fatalf("node state has activation height %d, want %d", got, lendingAccountingTestActivation)
	}
	copied, err := node.state.Copy()
	if err != nil {
		t.Fatalf("copy state: %v", err)
	}
	if got := copied.lendingAccountingFixesFromHeight; got != lendingAccountingTestActivation {
		t.Fatalf("a copy of the state has activation height %d, want %d", got, lendingAccountingTestActivation)
	}
	if err := node.rebuildStateProcessorLocked(node.state.Trie.Root()); err != nil {
		t.Fatalf("rebuild state processor: %v", err)
	}
	if got := node.state.lendingAccountingFixesFromHeight; got != lendingAccountingTestActivation {
		t.Fatalf("a rebuilt state processor has activation height %d, want %d", got, lendingAccountingTestActivation)
	}
}

// developerFeeCounterAfterBorrow runs one borrow that carries the pool's developer
// fee through the transaction pipeline at the given height and returns what the
// pool holds as withdrawable developer fees, and what the collector was paid.
func developerFeeCounterAfterBorrow(t *testing.T, gate, height uint64) (counter, collected *big.Int) {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	tr, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := NewStateProcessor(tr)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	sp.SetLendingRiskParameters(lending.RiskParameters{MaxLTV: 7_500, LiquidationThreshold: 8_000, DeveloperFeeCapBps: 500})
	sp.SetLendingAccrualConfig(0, 0, lending.DefaultInterestModel)
	collectorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	collector := collectorKey.PubKey().Address()
	sp.SetLendingDeveloperFee(100, collector)
	sp.SetLendingAccountingFixesActivationHeight(gate)

	supplier, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	borrower, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	collateral := mustBigInt(t, "50000000000000000000000")
	if err := sp.setAccount(supplier.PubKey().Address().Bytes(), &types.Account{BalanceNHB: mustBigInt(t, "10000000000000000000000"), BalanceZNHB: big.NewInt(0)}); err != nil {
		t.Fatal(err)
	}
	if err := sp.setAccount(borrower.PubKey().Address().Bytes(), &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: new(big.Int).Set(collateral)}); err != nil {
		t.Fatal(err)
	}
	if err := sp.setAccount(collector.Bytes(), &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit seeded state: %v", err)
	}
	setup := []*types.Transaction{
		mustSignLendingTx(t, supplier, types.TxTypeLendingSupplyNHB, 0, mustBigInt(t, "3000000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, borrower, types.TxTypeLendingDepositZNHB, 0, collateral, lendingNativePayload{PoolID: "default"}),
	}
	for i, tx := range setup {
		h := uint64(i + 1)
		sp.BeginBlock(h, time.Unix(int64(h), 0).UTC())
		if err := sp.ApplyTransaction(tx); err != nil {
			sp.EndBlock()
			t.Fatalf("setup tx %d: %v", i, err)
		}
		sp.EndBlock()
		if _, err := sp.Commit(h); err != nil {
			t.Fatalf("commit setup tx %d: %v", i, err)
		}
	}
	borrow := mustSignLendingTx(t, borrower, types.TxTypeLendingBorrowNHB, 1, mustBigInt(t, "1000000000000000000000"), lendingNativePayload{PoolID: "default", UseDeveloperFee: true})
	borrowAt(t, sp, borrower, borrow, height)

	manager := nhbstate.NewManager(sp.Trie)
	fees, _, err := manager.LendingGetFeeAccrual("default")
	counter = big.NewInt(0)
	if err == nil && fees != nil && fees.DeveloperFeesWei != nil {
		counter = fees.DeveloperFeesWei
	}
	acc, err := manager.GetAccount(collector.Bytes())
	if err != nil {
		t.Fatalf("collector account: %v", err)
	}
	return counter, acc.BalanceNHB
}

// PL-R5T-52 through the transaction pipeline: a 100 basis point fee on a borrow of
// 1,000 NHB is paid to the collector at once, and from the height it is not also
// counted as withdrawable.
func TestBorrowDeveloperFeeIsNotCountedAsWithdrawableFromTheAccountingFixesHeight(t *testing.T) {
	fee := mustBigInt(t, "10000000000000000000")
	cases := []struct {
		name    string
		gate    uint64
		height  uint64
		counted bool
	}{
		{"DefaultOffKeepsTheCounter", 0, 10_000_000, true},
		{"BelowActivationKeepsTheCounter", lendingAccountingTestActivation, lendingAccountingTestActivation - 1, true},
		{"AtActivationDropsTheCounter", lendingAccountingTestActivation, lendingAccountingTestActivation, false},
		{"AboveActivationDropsTheCounter", lendingAccountingTestActivation, lendingAccountingTestActivation + 40, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counter, collected := developerFeeCounterAfterBorrow(t, tc.gate, tc.height)
			if collected.Cmp(fee) != 0 {
				t.Fatalf("the collector was paid %s at the borrow, want the %s fee in every case", collected, fee)
			}
			want := big.NewInt(0)
			if tc.counted {
				want = fee
			}
			if counter.Cmp(want) != 0 {
				t.Fatalf("withdrawable developer fees are %s, want %s", counter, want)
			}
		})
	}
}
