package core

import (
	"math/big"
	"testing"
)

// PL-EXT-17: the per-pair cap the base reward has always had is off on the live chain
// (the relaunch genesis stores none). From the configured height the setting
// supplies it.

// pingPong moves a 10,000 NHB stack 0 -> 1 -> 0 -> 1 and settles; each transfer would
// earn the full 50 ZNHB per-transaction reward.
func pingPong(t *testing.T, env *abuseEnv) *big.Int {
	t.Helper()
	stack := znhb(10_000)
	env.fundNHB(0, stack)
	env.pay(0, 1, stack)
	env.pay(1, 0, stack)
	env.pay(0, 1, stack)
	env.settle()
	return env.totalZNHB(0, 1)
}

func TestLoyaltyCounterpartyCapOffLeavesPairPassingUncapped(t *testing.T) {
	env := newAbuseEnv(t, 2, nil, 10_000)
	// the setting is 0
	if got := pingPong(t, env); got.Cmp(znhb(150)) != 0 {
		t.Fatalf("setting off: the pair earned %s, want 150 ZNHB (three full rewards)", got)
	}
}

func TestLoyaltyCounterpartyCapOnLimitsWhatOnePairEarnsPerDay(t *testing.T) {
	env := newAbuseEnv(t, 2, nil, 10_000)
	env.sp.SetLoyaltyCounterpartyCap(100, znhb(80))
	if got := pingPong(t, env); got.Cmp(znhb(80)) != 0 {
		t.Fatalf("setting on: the pair earned %s, want exactly the cap of 80 ZNHB", got)
	}
}

func TestLoyaltyCounterpartyCapIsNotActiveBeforeItsHeight(t *testing.T) {
	env := newAbuseEnv(t, 2, nil, 10_000) // the block is at height 100
	env.sp.SetLoyaltyCounterpartyCap(101, znhb(80))
	if got := pingPong(t, env); got.Cmp(znhb(150)) != 0 {
		t.Fatalf("below the height the pair earned %s, want 150 ZNHB", got)
	}
}

func TestLoyaltyCounterpartyCapActiveAtExactlyItsHeight(t *testing.T) {
	env := newAbuseEnv(t, 2, nil, 10_000)
	env.sp.SetLoyaltyCounterpartyCap(100, znhb(80))
	if cap := env.sp.loyaltyCounterpartyCapActive(); cap == nil || cap.Cmp(znhb(80)) != 0 {
		t.Fatalf("not active at the height itself: %v", cap)
	}
}

// A cap the stored configuration sets is left as it is, the setting never raises it.
func TestLoyaltyCounterpartyCapLeavesAStoredCapAlone(t *testing.T) {
	env := newAbuseEnv(t, 2, znhb(60), 10_000)
	env.sp.SetLoyaltyCounterpartyCap(100, znhb(80))
	if got := pingPong(t, env); got.Cmp(znhb(60)) != 0 {
		t.Fatalf("the pair earned %s, want the stored cap of 60 ZNHB", got)
	}
}

func TestLoyaltyCounterpartyCapAZeroAmountIsOff(t *testing.T) {
	for _, amount := range []*big.Int{nil, big.NewInt(0), big.NewInt(-5)} {
		env := newAbuseEnv(t, 2, nil, 10_000)
		env.sp.SetLoyaltyCounterpartyCap(100, amount)
		if env.sp.loyaltyCounterpartyCapActive() != nil {
			t.Fatalf("amount %v: a cap is reported in force", amount)
		}
		if got := pingPong(t, env); got.Cmp(znhb(150)) != 0 {
			t.Fatalf("amount %v: the pair earned %s, want 150 ZNHB", amount, got)
		}
	}
}

// An amount with no height is off as well.
func TestLoyaltyCounterpartyCapAZeroHeightIsOff(t *testing.T) {
	env := newAbuseEnv(t, 2, nil, 10_000)
	env.sp.SetLoyaltyCounterpartyCap(0, znhb(80))
	if env.sp.loyaltyCounterpartyCapActive() != nil {
		t.Fatalf("a cap is reported in force with the height at zero")
	}
	if got := pingPong(t, env); got.Cmp(znhb(150)) != 0 {
		t.Fatalf("the pair earned %s, want 150 ZNHB", got)
	}
}

// The processor owns its amount: changing the caller's afterwards changes nothing.
func TestLoyaltyCounterpartyCapSetterCopiesTheAmount(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	amount := znhb(5)
	sp.SetLoyaltyCounterpartyCap(77, amount)
	amount.SetInt64(1)
	if sp.loyaltyCounterpartyCapWei.Cmp(znhb(5)) != 0 {
		t.Fatalf("the processor shares the caller's amount: %s", sp.loyaltyCounterpartyCapWei)
	}
}

// What the cap is not: NHB passed along a chain of fresh accounts earns at every hop,
// because every hop is a new pair. This pins the limit so the cap is never mistaken
// for the fix of the fresh-account chain (TestPLEXT17_Mitigation_AcceptanceTest, which
// stays skipped until the owner picks one).
func TestLoyaltyCounterpartyCapDoesNotStopAFreshAccountChain(t *testing.T) {
	env := newAbuseEnv(t, 6, nil, 10_000)
	env.sp.SetLoyaltyCounterpartyCap(100, znhb(80))
	stack := znhb(10_000)
	env.fundNHB(0, stack)
	env.passAlong(0, 5, stack)
	env.settle()
	if total := env.totalZNHB(0, 5); total.Cmp(znhb(250)) != 0 {
		t.Fatalf("a chain of fresh accounts earned %s ZNHB, want 250 (the cap does not apply to it)", total)
	}
}

// The configuration the RPC reads is the stored one.
func TestLoyaltyCounterpartyCapDoesNotChangeTheStoredConfiguration(t *testing.T) {
	env := newAbuseEnv(t, 2, nil, 10_000)
	env.sp.SetLoyaltyCounterpartyCap(100, znhb(80))
	cfg, err := env.sp.LoyaltyGlobalConfig()
	if err != nil || cfg == nil {
		t.Fatalf("config: %v %v", cfg, err)
	}
	if cfg.DailyCapCounterparty.Sign() != 0 {
		t.Fatalf("the stored configuration shows a cap of %s", cfg.DailyCapCounterparty)
	}
	view, err := loyaltyRewardState{env.sp}.LoyaltyGlobalConfig()
	if err != nil || view.DailyCapCounterparty.Cmp(znhb(80)) != 0 {
		t.Fatalf("the reward path's view: %v %v, want cap 80", view, err)
	}
	// and the reward path's view is a copy: the stored value is untouched afterwards
	again, _ := env.sp.LoyaltyGlobalConfig()
	if again.DailyCapCounterparty.Sign() != 0 {
		t.Fatalf("the override leaked into the stored configuration")
	}
}

func TestLoyaltyCounterpartyCapIsCarriedByCopy(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	sp.SetLoyaltyCounterpartyCap(77, znhb(5))
	dup, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if dup.loyaltyCounterpartyCapFromHeight != 77 || dup.loyaltyCounterpartyCapWei == nil || dup.loyaltyCounterpartyCapWei.Cmp(znhb(5)) != 0 {
		t.Fatalf("Copy dropped the setting: %d %v", dup.loyaltyCounterpartyCapFromHeight, dup.loyaltyCounterpartyCapWei)
	}
	// a copy owns its amount
	dup.loyaltyCounterpartyCapWei.SetInt64(1)
	if sp.loyaltyCounterpartyCapWei.Cmp(znhb(5)) != 0 {
		t.Fatalf("the copy shares the amount with the original")
	}
}

func TestNodeLoyaltyCounterpartyCap_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetLoyaltyCounterpartyCap(9, znhb(7))
	node.stateMu.Lock()
	gotH, gotW := node.state.loyaltyCounterpartyCapFromHeight, node.state.loyaltyCounterpartyCapWei
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH, rebuiltW := node.state.loyaltyCounterpartyCapFromHeight, node.state.loyaltyCounterpartyCapWei
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 9 || gotW == nil || gotW.Cmp(znhb(7)) != 0 {
		t.Fatalf("setter reached the processor as %d %v, want 9 and 7 ZNHB", gotH, gotW)
	}
	if rebuiltH != 9 || rebuiltW == nil || rebuiltW.Cmp(znhb(7)) != 0 {
		t.Fatalf("a rebuilt processor lost the setting: %d %v", rebuiltH, rebuiltW)
	}
}
