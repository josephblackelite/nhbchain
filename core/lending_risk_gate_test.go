package core

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

// PL-DC-19: the limits the node's [lending] configuration names (liquidation bonus,
// borrow caps, per-action pauses, circuit breaker, supply and collateral caps) were
// never handed to the engine. From LendingRiskActivationHeight they are.

const lendingRiskTestActivation = 100

// everyLimit sets every limit the gate controls, so a difference shows wherever one leaks.
func everyLimit() lending.RiskParameters {
	return lending.RiskParameters{
		MaxLTV:               7_500,
		LiquidationThreshold: 8_000,
		LiquidationBonus:     500,
		CircuitBreakerActive: true,
		BorrowCaps:           lending.BorrowCaps{PerBlock: big.NewInt(1), Total: big.NewInt(1), UtilisationBps: 1},
		Pauses:               lending.ActionPauses{Supply: true, Borrow: true, Repay: true, Liquidate: true},
		MaxTotalSupply:       big.NewInt(1),
		MaxTotalCollateral:   big.NewInt(1),
	}
}

func TestRiskLimitsAreAppliedToTheEngineOnlyFromTheRiskHeight(t *testing.T) {
	for _, tc := range []struct {
		name    string
		gate    uint64
		height  uint64
		inForce bool
	}{
		{"default off", 0, 10_000_000, false},
		{"below the height", lendingRiskTestActivation, lendingRiskTestActivation - 1, false},
		{"at the height", lendingRiskTestActivation, lendingRiskTestActivation, true},
		{"above the height", lendingRiskTestActivation, lendingRiskTestActivation + 40, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := newRewardTestState(t)
			sp.SetLendingRiskParameters(everyLimit())
			sp.SetLendingRiskActivationHeight(tc.gate)
			sp.BeginBlock(tc.height, time.Unix(int64(tc.height), 0).UTC())
			defer sp.EndBlock()
			got := sp.effectiveLendingRiskParameters()
			if tc.inForce {
				want := everyLimit()
				if got.LiquidationBonus != want.LiquidationBonus || !got.CircuitBreakerActive || got.Pauses != want.Pauses ||
					got.BorrowCaps.UtilisationBps != 1 || got.BorrowCaps.PerBlock == nil || got.BorrowCaps.Total == nil ||
					got.MaxTotalSupply == nil || got.MaxTotalCollateral == nil {
					t.Fatalf("from the height the engine must see every limit, got %+v", got)
				}
			} else if got.LiquidationBonus != 0 || got.CircuitBreakerActive || got.Pauses != (lending.ActionPauses{}) ||
				got.BorrowCaps.PerBlock != nil || got.BorrowCaps.Total != nil || got.BorrowCaps.UtilisationBps != 0 ||
				got.MaxTotalSupply != nil || got.MaxTotalCollateral != nil {
				t.Fatalf("before the height the engine must see none of them, got %+v", got)
			}
			// What was applied before the gate existed is applied at every height.
			if got.MaxLTV != 7_500 || got.LiquidationThreshold != 8_000 {
				t.Fatalf("the loan-to-value limits changed: %d/%d", got.MaxLTV, got.LiquidationThreshold)
			}
		})
	}
}

// Through the transaction pipeline: a borrow pause set in the configuration stops a
// borrow only from the height.
func TestABorrowPauseStopsABorrowOnlyFromTheRiskHeight(t *testing.T) {
	for _, tc := range []struct {
		name    string
		height  uint64
		refused bool
	}{
		{"below the height the borrow goes through", lendingRiskTestActivation - 1, false},
		{"from the height it is refused", lendingRiskTestActivation, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp, a, _ := lendingTwoBorrowers(t, 0)
			sp.SetLendingRiskParameters(lending.RiskParameters{MaxLTV: 7_500, LiquidationThreshold: 8_000, Pauses: lending.ActionPauses{Borrow: true}})
			sp.SetLendingRiskActivationHeight(lendingRiskTestActivation)
			tx := mustSignLendingTx(t, a, types.TxTypeLendingBorrowNHB, 1, mustBigInt(t, "100000000000000000000"), lendingNativePayload{PoolID: "default"})
			sp.BeginBlock(tc.height, time.Unix(int64(tc.height), 0).UTC())
			defer sp.EndBlock()
			err := sp.ApplyTransaction(tx)
			if tc.refused {
				if !errors.Is(err, lending.ErrBorrowPaused) {
					t.Fatalf("got %v, want ErrBorrowPaused", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("below the height the pause is not in force: %v", err)
			}
		})
	}
}

// With the height at its default, setting every limit in the configuration changes
// nothing a block leaves behind: the same transactions give the same state root.
func TestSettingEveryRiskLimitWithTheHeightOffLeavesTheStateRootUnchanged(t *testing.T) {
	base := lendingRiskStateRoot(t, lending.RiskParameters{MaxLTV: 7_500, LiquidationThreshold: 8_000})
	loaded := lendingRiskStateRoot(t, everyLimitWithLTV())
	if base != loaded {
		t.Fatalf("with the height off, setting every limit changed the state root: %x vs %x", base, loaded)
	}
}

func everyLimitWithLTV() lending.RiskParameters {
	p := everyLimit()
	p.MaxLTV, p.LiquidationThreshold = 7_500, 8_000
	return p
}

// lendingRiskStateRoot replays a fixed lending history (supply, collateral, a flexible
// borrow, a fixed-term borrow, a repayment) under the given parameters and returns the
// state root. The keys are fixed so two runs are comparable.
func lendingRiskStateRoot(t *testing.T, params lending.RiskParameters) [32]byte {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	tr, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := NewStateProcessor(tr)
	if err != nil {
		t.Fatal(err)
	}
	sp.SetLendingRiskParameters(params)
	sp.SetLendingAccrualConfig(1_000, 0, lending.DefaultInterestModel)

	key := func(seed byte) *crypto.PrivateKey {
		raw := make([]byte, 32)
		raw[31] = seed
		k, err := crypto.PrivateKeyFromBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	supplier, borrower := key(1), key(2)
	collateral := mustBigInt(t, "50000000000000000000000")
	if err := sp.setAccount(supplier.PubKey().Address().Bytes(), &types.Account{BalanceNHB: mustBigInt(t, "10000000000000000000000"), BalanceZNHB: big.NewInt(0)}); err != nil {
		t.Fatal(err)
	}
	if err := sp.setAccount(borrower.PubKey().Address().Bytes(), &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: new(big.Int).Set(collateral)}); err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatal(err)
	}
	steps := []*types.Transaction{
		mustSignLendingTx(t, supplier, types.TxTypeLendingSupplyNHB, 0, mustBigInt(t, "3000000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, borrower, types.TxTypeLendingDepositZNHB, 0, collateral, lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, borrower, types.TxTypeLendingBorrowNHB, 1, mustBigInt(t, "100000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingFixedTermBorrowTx(t, borrower, 2, mustBigInt(t, "100000000000000000000"), 30),
		mustSignLendingTx(t, borrower, types.TxTypeLendingRepayNHB, 3, mustBigInt(t, "50000000000000000000"), lendingNativePayload{PoolID: "default"}),
	}
	for i, tx := range steps {
		h := uint64(10 + i)
		sp.BeginBlock(h, time.Unix(1_800_000_000+int64(h)*2, 0).UTC())
		err := sp.ApplyTransaction(tx)
		sp.EndBlock()
		if i < 3 && err != nil {
			t.Fatalf("history step %d: %v", i, err)
		}
		if _, err := sp.Commit(h); err != nil {
			t.Fatalf("commit step %d: %v", i, err)
		}
	}
	root := sp.CurrentRoot()
	var out [32]byte
	copy(out[:], root.Bytes())
	return out
}

func TestTheRiskLimitsInForceForTheNextBlockAreWhatTheNodeReports(t *testing.T) {
	node := newTestNode(t)
	given := everyLimit()
	node.SetLendingRiskParameters(given)
	// With the height at its default nothing is in force, whatever the file says.
	if got := node.LendingRiskParameters(); got.LiquidationBonus != 0 || got.CircuitBreakerActive || got.Pauses != (lending.ActionPauses{}) || got.MaxTotalSupply != nil || got.BorrowCaps.Total != nil {
		t.Fatalf("with the height off the node reports a limit in force: %+v", got)
	}
	// The node owns its copy of the limits: changing the caller's numbers later changes nothing.
	given.MaxTotalSupply.SetInt64(999)
	given.BorrowCaps.Total.SetInt64(999)
	node.SetLendingRiskActivationHeight(lendingRiskTestActivation)
	got := node.LendingRiskParameters()
	if got.LiquidationBonus != 0 || got.CircuitBreakerActive || got.Pauses != (lending.ActionPauses{}) || got.MaxTotalSupply != nil {
		t.Fatalf("before the height the node must report no limit in force, got %+v", got)
	}
	if got.MaxLTV != 7_500 {
		t.Fatalf("the node dropped the loan-to-value limit: %d", got.MaxLTV)
	}
	node.SetLendingRiskActivationHeight(1) // the next block is block 1 on a new chain
	got = node.LendingRiskParameters()
	if got.LiquidationBonus != 500 || !got.CircuitBreakerActive || !got.Pauses.Borrow || got.MaxTotalSupply == nil || got.BorrowCaps.Total == nil {
		t.Fatalf("from the height the node must report every limit, got %+v", got)
	}
	if got.MaxTotalSupply.Int64() != 1 || got.BorrowCaps.Total.Int64() != 1 {
		t.Fatalf("the node shares its numbers with the caller: supply cap %s, total cap %s", got.MaxTotalSupply, got.BorrowCaps.Total)
	}
}

func TestLendingRiskGateReachesBlockState(t *testing.T) {
	node := newTestNode(t)
	node.SetLendingRiskParameters(everyLimit())
	node.SetLendingRiskActivationHeight(lendingRiskTestActivation)
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	check := func(what string, sp *StateProcessor) {
		t.Helper()
		if sp.lendingRiskFromHeight != lendingRiskTestActivation {
			t.Fatalf("%s holds height %d, want %d", what, sp.lendingRiskFromHeight, lendingRiskTestActivation)
		}
		if sp.lendingParams.MaxTotalSupply == nil || sp.lendingParams.MaxTotalCollateral == nil || sp.lendingParams.BorrowCaps.Total == nil || sp.lendingParams.LiquidationBonus != 500 {
			t.Fatalf("%s lost a limit: %+v", what, sp.lendingParams)
		}
	}
	check("the node's state", node.state)
	copied, err := node.state.Copy()
	if err != nil {
		t.Fatal(err)
	}
	check("a copy of the state", copied)
	if err := node.rebuildStateProcessorLocked(node.state.Trie.Root()); err != nil {
		t.Fatal(err)
	}
	check("a rebuilt state processor", node.state)
}

func TestLendingRiskRefusalsAreClassifiedSoTheyNeverHaltABlock(t *testing.T) {
	for _, err := range []error{
		lending.ErrSupplyPaused, lending.ErrBorrowPaused, lending.ErrLiquidationsPaused, lending.ErrRepayPaused,
		lending.ErrBorrowCapPerBlock, lending.ErrBorrowCapGlobal, lending.ErrBorrowCapUtilisation,
		lending.ErrSupplyCapExceeded, lending.ErrCollateralCapExceeded, lending.ErrOracleStale, lending.ErrOracleDeviation,
	} {
		if got := classifyProposalError(err); got != proposalDispositionSkip {
			t.Errorf("%v is classified %v, want skip", err, got)
		}
	}
}

// A refused supply and a valid pool creation in one proposal: the block is built with
// the valid one and a second node reaches the same root.
func TestCreateBlockWithAPausedSupplyStillProducesABlock(t *testing.T) {
	build := func() *Node {
		node := newTestNode(t)
		node.SetLendingRiskParameters(lending.RiskParameters{MaxLTV: 7_500, LiquidationThreshold: 8_000, Pauses: lending.ActionPauses{Supply: true}})
		node.SetLendingRiskActivationHeight(1)
		return node
	}
	proposer, validator := build(), build()
	supplier, creator := newKey(t), newKey(t)
	paused := mustSignLendingTx(t, supplier, types.TxTypeLendingSupplyNHB, 0, big.NewInt(1_000), lendingNativePayload{PoolID: "default"})
	create := lendingCreatePoolTx(t, 0, "good-pool")
	if err := create.Sign(creator.PrivateKey); err != nil {
		t.Fatal(err)
	}
	block, err := proposer.CreateBlock([]*types.Transaction{paused, create})
	if err != nil {
		t.Fatalf("a refused supply must not stop the block: %v", err)
	}
	if len(block.Transactions) != 1 || block.Transactions[0].Type != types.TxTypeLendingCreatePool {
		t.Fatalf("expected only the pool creation in the block, got %d txs", len(block.Transactions))
	}
	if err := validator.ValidateBlock(block); err != nil {
		t.Fatalf("validator rejected the block: %v", err)
	}
	if err := proposer.CommitBlock(block); err != nil {
		t.Fatal(err)
	}
	if err := validator.CommitBlock(block); err != nil {
		t.Fatal(err)
	}
	if proposer.state.CurrentRoot() != validator.state.CurrentRoot() {
		t.Fatalf("proposer and validator diverged")
	}
}
