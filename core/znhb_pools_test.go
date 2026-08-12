package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func newZNHBPoolsStateProcessor(t *testing.T) *StateProcessor {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("create trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	return sp
}

func TestEnsureZNHBPoolsBootstrapped_SplitsCorrectly(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	salePool, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		t.Fatalf("read sale pool: %v", err)
	}
	if salePool.Cmp(znhbExpectedSalePoolWei) != 0 {
		t.Fatalf("sale pool = %s, want %s", salePool, znhbExpectedSalePoolWei)
	}
	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read reward pool: %v", err)
	}
	if rewardPool.Cmp(znhbExpectedRewardPoolWei) != 0 {
		t.Fatalf("reward pool = %s, want %s", rewardPool, znhbExpectedRewardPoolWei)
	}
	cumulative, err := manager.ZNHBCumulativeSaleDistributed()
	if err != nil {
		t.Fatalf("read cumulative sold: %v", err)
	}
	if cumulative.Sign() != 0 {
		t.Fatalf("cumulative sold = %s, want 0 immediately after bootstrap", cumulative)
	}

	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant should hold immediately after bootstrap: %v", err)
	}
}

func TestEnsureZNHBPoolsBootstrapped_IsIdempotent(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	// Simulate a purchase having advanced the counter and drawn down the
	// sale pool, so a second bootstrap call would be destructive if it
	// weren't idempotent.
	if err := manager.ZNHBSetCumulativeSaleDistributed(big.NewInt(12345)); err != nil {
		t.Fatalf("simulate sale progress: %v", err)
	}

	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("second bootstrap call should be a no-op, got error: %v", err)
	}

	cumulative, err := manager.ZNHBCumulativeSaleDistributed()
	if err != nil {
		t.Fatalf("read cumulative sold: %v", err)
	}
	if cumulative.Cmp(big.NewInt(12345)) != 0 {
		t.Fatalf("cumulative sold = %s, want 12345 (bootstrap must not re-run and reset progress)", cumulative)
	}
}

func TestEnsureZNHBPoolsBootstrapped_SplitsWhateverLiveBalanceIs(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	// A balance that has drifted from any frozen genesis snapshot -- e.g.
	// real buyZNHB purchases moved it before the node's first restart
	// under bootstrap-aware code. Bootstrap must split whatever this
	// actually is, not refuse to start the node.
	drift, ok := new(big.Int).SetString("21171800000000000000000", 10)
	if !ok {
		t.Fatalf("parse drift constant")
	}
	liveBalance := new(big.Int).Sub(znhbExpectedTotalSupplyWei, drift)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(liveBalance),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap should succeed against a drifted live balance: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	wantReward := znhbPoolRewardShare(liveBalance)
	wantSale := new(big.Int).Sub(liveBalance, wantReward)

	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read reward pool: %v", err)
	}
	if rewardPool.Cmp(wantReward) != 0 {
		t.Fatalf("reward pool = %s, want %s (20%% of live balance)", rewardPool, wantReward)
	}
	salePool, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		t.Fatalf("read sale pool: %v", err)
	}
	if salePool.Cmp(wantSale) != 0 {
		t.Fatalf("sale pool = %s, want %s (remainder of live balance)", salePool, wantSale)
	}
	if new(big.Int).Add(salePool, rewardPool).Cmp(liveBalance) != 0 {
		t.Fatalf("sale pool + reward pool must sum to exactly the live balance")
	}

	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant should hold immediately after bootstrap: %v", err)
	}
}

func TestEnsureZNHBPoolsBootstrapped_HardFailsOnZeroBalance(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.EnsureZNHBPoolsBootstrapped(); err == nil {
		t.Fatalf("expected a hard failure for a zero admin wallet balance, got nil error")
	}

	manager := nhbstate.NewManager(sp.Trie)
	bootstrapped, err := manager.ZNHBPoolsBootstrapped()
	if err != nil {
		t.Fatalf("read bootstrap flag: %v", err)
	}
	if bootstrapped {
		t.Fatalf("bootstrap flag must not be set after a failed bootstrap attempt")
	}
}

func TestEnsureZNHBPoolsBootstrapped_NoAdminWalletIsNoop(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	// hasAdminWallet defaults to false -- neither function should error.
	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap without an admin wallet should be a silent no-op, got: %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant check without an admin wallet should be a silent no-op, got: %v", err)
	}
}

func TestCheckZNHBSupplyInvariant_DetectsViolation(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}
	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// Simulate a consensus bug: silently inflate the sale pool sub-ledger
	// without a corresponding admin-wallet balance change.
	manager := nhbstate.NewManager(sp.Trie)
	salePool, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		t.Fatalf("read sale pool: %v", err)
	}
	corrupted := new(big.Int).Add(salePool, big.NewInt(1))
	if err := manager.ZNHBSetSalePoolBalance(corrupted); err != nil {
		t.Fatalf("corrupt sale pool: %v", err)
	}

	if err := sp.CheckZNHBSupplyInvariant(); err == nil {
		t.Fatalf("expected the supply invariant check to catch a desynced pool balance")
	}
}
