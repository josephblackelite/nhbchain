package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/loyalty"
)

// PL-CLM-02: a floor under the daily budget for base rewards. The budget is the
// smaller of a USD cap and a share of the last seven days of fees, so with no
// fees it is zero and every reward is pro-rated to nothing. The floor lifts the
// day's budget to a configured amount, never above the daily USD cap.

var floorDay = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func znhb(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), big.NewInt(1_000_000_000_000_000_000))
}

// configureFloorLoyalty writes a loyalty config with a daily USD cap (0 = none),
// the price guard off (so the price is 1 USD per ZNHB) or on with no price
// available, and no fee share, so the fee-based term of the budget is zero.
func configureFloorLoyalty(t *testing.T, manager *nhbstate.Manager, treasury [20]byte, dailyCapUSD uint64, guardOn bool) {
	t.Helper()
	cfg := (&loyalty.GlobalConfig{
		Active:       true,
		Treasury:     treasury[:],
		BaseBps:      50,
		MinSpend:     big.NewInt(0),
		CapPerTx:     big.NewInt(0),
		DailyCapUser: big.NewInt(0),
		Dynamic: loyalty.DynamicConfig{
			DailyCapPctOf7dFeesBps: 6_000,
			DailyCapUsd:            dailyCapUSD,
			EnableProRate:          true,
			EnableProRateSet:       true,
			PriceGuard: loyalty.PriceGuardConfig{
				Enabled:   guardOn,
				PricePair: "ZNHB/USD",
			},
		},
	}).Normalize()
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		t.Fatalf("set loyalty config: %v", err)
	}
}

func remainingBudget(t *testing.T, manager *nhbstate.Manager, floor *big.Int) *big.Int {
	t.Helper()
	got, _, err := manager.GetRemainingDailyBudgetZNHBWithFloor(floorDay, floor)
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	return got
}

func TestLoyaltyBudgetFloor_ZeroFeesGiveZeroBudgetWithoutAFloor(t *testing.T) {
	_, manager := newTestStateProcessor(t)
	var treasury [20]byte
	treasury[19] = 0x01
	configureFloorLoyalty(t, manager, treasury, 0, false)
	if got := remainingBudget(t, manager, nil); got.Sign() != 0 {
		t.Fatalf("with no fees and no floor the budget is %s, want 0 (this is the live condition)", got)
	}
}

func TestLoyaltyBudgetFloor_LiftsTheBudgetAndPaidAmountComesOffIt(t *testing.T) {
	_, manager := newTestStateProcessor(t)
	var treasury [20]byte
	treasury[19] = 0x01
	configureFloorLoyalty(t, manager, treasury, 0, false)
	if got := remainingBudget(t, manager, znhb(1_000)); got.Cmp(znhb(1_000)) != 0 {
		t.Fatalf("budget with a 1,000 ZNHB floor = %s, want 1,000 ZNHB", got)
	}
	if _, err := manager.AddPaidTodayZNHB(floorDay, znhb(400)); err != nil {
		t.Fatalf("record paid: %v", err)
	}
	if got := remainingBudget(t, manager, znhb(1_000)); got.Cmp(znhb(600)) != 0 {
		t.Fatalf("after 400 ZNHB paid the remaining budget = %s, want 600 ZNHB", got)
	}
}

func TestLoyaltyBudgetFloor_NilOrNonPositiveFloorIsThePlainFunction(t *testing.T) {
	_, manager := newTestStateProcessor(t)
	var treasury [20]byte
	treasury[19] = 0x01
	configureFloorLoyalty(t, manager, treasury, 5_000, false)
	plain, _, err := manager.GetRemainingDailyBudgetZNHB(floorDay)
	if err != nil {
		t.Fatalf("plain budget: %v", err)
	}
	for name, floor := range map[string]*big.Int{"nil": nil, "zero": big.NewInt(0), "negative": big.NewInt(-5)} {
		if got := remainingBudget(t, manager, floor); got.Cmp(plain) != 0 {
			t.Fatalf("%s floor changed the budget: %s vs %s", name, got, plain)
		}
	}
}

// The floor stays inside the existing daily USD cap: at one USD per ZNHB a cap of
// 5 USD is 5 ZNHB, so a 100 ZNHB floor is limited to 5, and a 2 ZNHB floor is not.
func TestLoyaltyBudgetFloor_NeverPassesTheDailyUSDCap(t *testing.T) {
	_, manager := newTestStateProcessor(t)
	var treasury [20]byte
	treasury[19] = 0x01
	configureFloorLoyalty(t, manager, treasury, 5, false)
	if got := remainingBudget(t, manager, znhb(100)); got.Cmp(znhb(5)) != 0 {
		t.Fatalf("a 100 ZNHB floor under a 5 USD cap gave %s, want 5 ZNHB", got)
	}
	if got := remainingBudget(t, manager, znhb(2)); got.Cmp(znhb(2)) != 0 {
		t.Fatalf("a 2 ZNHB floor under a 5 USD cap gave %s, want 2 ZNHB", got)
	}
}

// A budget the fees already put above the floor is left alone.
func TestLoyaltyBudgetFloor_DoesNotLowerAHigherBudget(t *testing.T) {
	_, manager := newTestStateProcessor(t)
	var treasury [20]byte
	treasury[19] = 0x01
	configureFloorLoyalty(t, manager, treasury, 0, false)
	tracker := nhbstate.NewRollingFees(manager)
	if err := tracker.AddDay(floorDay, big.NewInt(0), znhb(10_000)); err != nil {
		t.Fatalf("add fees: %v", err)
	}
	// 60 percent of 10,000 ZNHB of fees is 6,000; a cap of 0 USD is normalised to
	// the default 5,000 USD, so the plain budget is 5,000 ZNHB, above the floor.
	plain := remainingBudget(t, manager, nil)
	if plain.Cmp(znhb(5_000)) != 0 {
		t.Fatalf("test setup: plain budget = %s, want 5,000 ZNHB", plain)
	}
	if got := remainingBudget(t, manager, znhb(1_000)); got.Cmp(plain) != 0 {
		t.Fatalf("budget with fees above the floor = %s, want the plain %s", got, plain)
	}
}

// With a USD cap set and no usable price the cap cannot be expressed in ZNHB, so
// the floor is not applied (it fails closed, like the budget itself).
func TestLoyaltyBudgetFloor_NotAppliedWhenTheCapIsSetAndThereIsNoPrice(t *testing.T) {
	_, manager := newTestStateProcessor(t)
	var treasury [20]byte
	treasury[19] = 0x01
	configureFloorLoyalty(t, manager, treasury, 5_000, true) // guard on, no price recorded
	if got := remainingBudget(t, manager, znhb(1_000)); got.Sign() != 0 {
		t.Fatalf("floor applied without a price under a USD cap: %s", got)
	}
}

// Through the settlement a block runs: with the floor in force the day's queued
// rewards are paid, from the treasury, up to the floor; without it nothing is.
func TestLoyaltyBudgetFloor_SettlementPaysUpToTheFloor(t *testing.T) {
	settle := func(t *testing.T, height uint64, fromHeight uint64, floor *big.Int, demands ...int64) (paid []*big.Int, treasuryLeft *big.Int) {
		t.Helper()
		sp, manager := newTestStateProcessor(t)
		var treasury [20]byte
		treasury[19] = 0x01
		configureFloorLoyalty(t, manager, treasury, 0, false)
		setAccountBalance(t, sp, treasury, znhb(100_000))
		sp.SetLoyaltyBudgetFloor(fromHeight, floor)
		sp.BeginBlock(height, floorDay)
		recipients := make([][20]byte, len(demands))
		for i, d := range demands {
			recipients[i][19] = byte(0x10 + i)
			sp.blockCtx.PendingRewards.AddPendingReward(nhbstate.PendingReward{Recipient: recipients[i], AmountZNHB: znhb(d)})
		}
		sp.EndBlockRewards(floorDay)
		for _, r := range recipients {
			acc, err := sp.getAccount(r[:])
			if err != nil {
				t.Fatalf("load recipient: %v", err)
			}
			paid = append(paid, new(big.Int).Set(acc.BalanceZNHB))
		}
		acc, err := sp.getAccount(treasury[:])
		if err != nil {
			t.Fatalf("load treasury: %v", err)
		}
		return paid, new(big.Int).Set(acc.BalanceZNHB)
	}
	sum := func(xs []*big.Int) *big.Int {
		total := big.NewInt(0)
		for _, x := range xs {
			total.Add(total, x)
		}
		return total
	}

	t.Run("no floor: zero fees pays nothing", func(t *testing.T) {
		paid, left := settle(t, 100, 0, nil, 40, 60)
		if sum(paid).Sign() != 0 || left.Cmp(znhb(100_000)) != 0 {
			t.Fatalf("paid %s, treasury %s", sum(paid), left)
		}
	})
	t.Run("floor covers the demand: paid in full", func(t *testing.T) {
		paid, left := settle(t, 100, 100, znhb(1_000), 40, 60)
		if paid[0].Cmp(znhb(40)) != 0 || paid[1].Cmp(znhb(60)) != 0 {
			t.Fatalf("paid %s and %s, want 40 and 60 ZNHB", paid[0], paid[1])
		}
		if left.Cmp(znhb(99_900)) != 0 {
			t.Fatalf("treasury left %s, want 99,900 ZNHB", left)
		}
	})
	t.Run("floor below the demand: pro-rated to the floor", func(t *testing.T) {
		paid, _ := settle(t, 100, 100, znhb(50), 40, 60)
		if got := sum(paid); got.Cmp(znhb(50)) != 0 {
			t.Fatalf("paid %s in total, want the 50 ZNHB floor", got)
		}
	})
	t.Run("the block before the height: nothing", func(t *testing.T) {
		paid, _ := settle(t, 99, 100, znhb(1_000), 40, 60)
		if sum(paid).Sign() != 0 {
			t.Fatalf("paid %s before the activation height", sum(paid))
		}
	})
	t.Run("the height itself: paid", func(t *testing.T) {
		paid, _ := settle(t, 100, 100, znhb(1_000), 40)
		if paid[0].Cmp(znhb(40)) != 0 {
			t.Fatalf("paid %s at the activation height, want 40 ZNHB", paid[0])
		}
	})
	t.Run("a zero height keeps the floor off", func(t *testing.T) {
		paid, _ := settle(t, 100, 0, znhb(1_000), 40)
		if paid[0].Sign() != 0 {
			t.Fatalf("paid %s with the height at zero", paid[0])
		}
	})
}

// A zero, negative or missing amount is no floor at all, whatever the height.
func TestLoyaltyBudgetFloor_SetterIgnoresANonPositiveAmount(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	for name, amount := range map[string]*big.Int{"nil": nil, "zero": big.NewInt(0), "negative": big.NewInt(-3)} {
		sp.SetLoyaltyBudgetFloor(7, znhb(10))
		sp.SetLoyaltyBudgetFloor(7, amount)
		if sp.loyaltyBudgetFloorWei != nil {
			t.Fatalf("%s amount left a floor of %s in place", name, sp.loyaltyBudgetFloorWei)
		}
		if got := sp.loyaltyBudgetFloorAtHeight(1_000); got != nil {
			t.Fatalf("%s amount gave a floor of %s at height 1000", name, got)
		}
	}
}

func TestLoyaltyBudgetFloor_TheSettingSurvivesCopyAndIsNotShared(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	original := znhb(10)
	sp.SetLoyaltyBudgetFloor(7, original)
	copied, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.loyaltyBudgetFloorFromHeight != 7 || copied.loyaltyBudgetFloorWei == nil || copied.loyaltyBudgetFloorWei.Cmp(znhb(10)) != 0 {
		t.Fatalf("Copy lost the floor: height=%d wei=%v", copied.loyaltyBudgetFloorFromHeight, copied.loyaltyBudgetFloorWei)
	}
	original.SetInt64(1) // changing the caller's number must not move either processor's floor
	if sp.loyaltyBudgetFloorWei.Cmp(znhb(10)) != 0 || copied.loyaltyBudgetFloorWei.Cmp(znhb(10)) != 0 {
		t.Fatalf("the floor aliases the caller's big.Int")
	}
	copied.loyaltyBudgetFloorWei.SetInt64(2)
	if sp.loyaltyBudgetFloorWei.Cmp(znhb(10)) != 0 {
		t.Fatalf("the copy's floor aliases the original's")
	}
}

func TestNodeLoyaltyBudgetFloor_SetterRebuildAndStatus(t *testing.T) {
	node := newTestNode(t)
	node.SetLoyaltyBudgetFloor(1, znhb(25))
	node.stateMu.Lock()
	gotH, gotWei := node.state.loyaltyBudgetFloorFromHeight, node.state.loyaltyBudgetFloorWei
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH, rebuiltWei := node.state.loyaltyBudgetFloorFromHeight, node.state.loyaltyBudgetFloorWei
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 1 || gotWei == nil || gotWei.Cmp(znhb(25)) != 0 {
		t.Fatalf("setter left processor at %d/%v", gotH, gotWei)
	}
	if rebuiltH != 1 || rebuiltWei == nil || rebuiltWei.Cmp(znhb(25)) != 0 {
		t.Fatalf("a rebuilt processor lost the floor: %d/%v", rebuiltH, rebuiltWei)
	}

	// nhb_getLoyaltyBudgetStatus must show what the next block will settle with.
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	var treasury [20]byte
	treasury[19] = 0x01
	configureFloorLoyalty(t, manager, treasury, 0, false)
	node.stateMu.Unlock()
	status, err := node.LoyaltyBudgetStatus()
	if err != nil {
		t.Fatalf("budget status: %v", err)
	}
	if status.BudgetRemainingZNHB.Cmp(znhb(25)) != 0 {
		t.Fatalf("status budget = %s, want the 25 ZNHB floor", status.BudgetRemainingZNHB)
	}
	node.SetLoyaltyBudgetFloor(0, nil)
	status, err = node.LoyaltyBudgetStatus()
	if err != nil {
		t.Fatalf("budget status: %v", err)
	}
	if status.BudgetRemainingZNHB.Sign() != 0 {
		t.Fatalf("status budget = %s with the floor off, want 0", status.BudgetRemainingZNHB)
	}
}
