package state

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/native/loyalty"
	"nhbchain/native/swap"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func TestLoyaltyEngineStateStepLimitsMovement(t *testing.T) {
	state := &LoyaltyEngineState{
		EffectiveBps:     40,
		TargetBps:        70,
		MinBps:           20,
		MaxBps:           90,
		SmoothingStepBps: 5,
	}
	changed := state.StepTowardsTarget()
	if !changed {
		t.Fatalf("expected effective bps to adjust")
	}
	if state.EffectiveBps != 45 {
		t.Fatalf("expected effective bps 45 after step, got %d", state.EffectiveBps)
	}
}

func TestLoyaltyEngineStateStepClampsBounds(t *testing.T) {
	state := &LoyaltyEngineState{
		EffectiveBps:     95,
		TargetBps:        60,
		MinBps:           30,
		MaxBps:           60,
		SmoothingStepBps: 20,
	}
	state.StepTowardsTarget()
	if state.EffectiveBps != 60 {
		t.Fatalf("expected clamp to max 60, got %d", state.EffectiveBps)
	}

	state = &LoyaltyEngineState{
		EffectiveBps:     25,
		TargetBps:        10,
		MinBps:           20,
		MaxBps:           80,
		SmoothingStepBps: 15,
	}
	state.StepTowardsTarget()
	if state.EffectiveBps != 20 {
		t.Fatalf("expected clamp to min 20, got %d", state.EffectiveBps)
	}
}

func TestNewLoyaltyEngineStateFromDynamicUsesDefaults(t *testing.T) {
	cfg := loyalty.DynamicConfig{
		TargetBps:        0,
		MinBps:           0,
		MaxBps:           0,
		SmoothingStepBps: 0,
	}
	state := NewLoyaltyEngineStateFromDynamic(cfg)
	if state == nil {
		t.Fatalf("expected state")
	}
	if state.TargetBps != loyalty.DefaultDynamicTargetBps {
		t.Fatalf("expected target bps %d, got %d", loyalty.DefaultDynamicTargetBps, state.TargetBps)
	}
	if state.EffectiveBps != loyalty.DefaultDynamicTargetBps {
		t.Fatalf("expected effective bps %d, got %d", loyalty.DefaultDynamicTargetBps, state.EffectiveBps)
	}
	if state.MinBps != loyalty.DefaultDynamicMinBps {
		t.Fatalf("expected min bps %d, got %d", loyalty.DefaultDynamicMinBps, state.MinBps)
	}
	if state.MaxBps != loyalty.DefaultDynamicMaxBps {
		t.Fatalf("expected max bps %d, got %d", loyalty.DefaultDynamicMaxBps, state.MaxBps)
	}
	if state.SmoothingStepBps != loyalty.DefaultDynamicSmoothingStepBps {
		t.Fatalf("expected smoothing step %d, got %d", loyalty.DefaultDynamicSmoothingStepBps, state.SmoothingStepBps)
	}
}

func TestLoyaltyEngineStateCanEmitHonoursCap(t *testing.T) {
	cap := big.NewInt(1_000)
	state := (&LoyaltyEngineState{
		YearlyCapZNHB: cap,
	}).Normalize()

	if ok, hit := state.CanEmit(big.NewInt(400)); !ok || hit {
		t.Fatalf("expected first emission to succeed without hitting cap: ok=%v hit=%v", ok, hit)
	}
	if want := big.NewInt(400); state.YtdEmissionsZNHB.Cmp(want) != 0 {
		t.Fatalf("unexpected ytd after first emission: got %s want %s", state.YtdEmissionsZNHB, want)
	}

	if ok, hit := state.CanEmit(big.NewInt(600)); !ok || !hit {
		t.Fatalf("expected second emission to reach cap: ok=%v hit=%v", ok, hit)
	}
	if want := big.NewInt(1_000); state.YtdEmissionsZNHB.Cmp(want) != 0 {
		t.Fatalf("unexpected ytd after hitting cap: got %s want %s", state.YtdEmissionsZNHB, want)
	}

	if ok, hit := state.CanEmit(big.NewInt(1)); ok || !hit {
		t.Fatalf("expected cap overrun to be rejected and mark cap hit: ok=%v hit=%v", ok, hit)
	}
	if want := big.NewInt(1_000); state.YtdEmissionsZNHB.Cmp(want) != 0 {
		t.Fatalf("expected ytd unchanged after rejection: got %s want %s", state.YtdEmissionsZNHB, want)
	}
}

func TestLoyaltyEngineStateCanEmitUnlimitedWhenCapUnset(t *testing.T) {
	state := (&LoyaltyEngineState{}).Normalize()
	if ok, hit := state.CanEmit(big.NewInt(500)); !ok || hit {
		t.Fatalf("expected emission to succeed with no cap: ok=%v hit=%v", ok, hit)
	}
	if want := big.NewInt(500); state.YtdEmissionsZNHB.Cmp(want) != 0 {
		t.Fatalf("unexpected ytd after emission: got %s want %s", state.YtdEmissionsZNHB, want)
	}
}

func newLoyaltyTestManager(t *testing.T) *Manager {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() {
		db.Close()
	})
	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	return NewManager(tr)
}

func TestGetRemainingDailyBudgetZNHB_UsesLastGoodPriceFallback(t *testing.T) {
	manager := newLoyaltyTestManager(t)
	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)

	cfg := (&loyalty.GlobalConfig{
		Active:   true,
		Treasury: make([]byte, 20),
		Dynamic: loyalty.DynamicConfig{
			DailyCapPctOf7dFeesBps: 5000,
			DailyCapUsd:            0,
			PriceGuard: loyalty.PriceGuardConfig{
				Enabled:                  true,
				PricePair:                "ZNHB/USD",
				TwapWindowSeconds:        60,
				PriceMaxAgeSeconds:       60,
				UseLastGoodPriceFallback: true,
				FallbackMinEmissionZNHB:  big.NewInt(0),
			},
		},
	}).Normalize()
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		t.Fatalf("set loyalty config: %v", err)
	}

	fees := NewRollingFees(manager)
	if err := fees.AddDay(now.AddDate(0, 0, -1), tokensToWei(0), tokensToWei(100)); err != nil {
		t.Fatalf("add rolling fees: %v", err)
	}

	staleProof := &swap.PriceProofRecord{Rate: big.NewRat(2, 1)}
	if err := manager.SwapPutPriceProof("ZNHB", staleProof); err != nil {
		t.Fatalf("put price proof: %v", err)
	}

	budget, fallback, err := manager.GetRemainingDailyBudgetZNHB(now)
	if err != nil {
		t.Fatalf("remaining budget: %v", err)
	}
	expected := tokensToWei(50)
	if budget.Cmp(expected) != 0 {
		t.Fatalf("unexpected budget: got %s want %s", budget.String(), expected.String())
	}
	if fallback == nil {
		t.Fatalf("expected fallback signal when price guard fails")
	}
	if fallback.Strategy != "last_good_price" {
		t.Fatalf("unexpected fallback strategy: %s", fallback.Strategy)
	}
	if fallback.Base != "ZNHB" {
		t.Fatalf("unexpected fallback base: %s", fallback.Base)
	}
	if fallback.BudgetZNHB.Cmp(budget) != 0 {
		t.Fatalf("expected fallback budget to match remaining budget")
	}
}

func TestGetRemainingDailyBudgetZNHB_MinEmissionFallback(t *testing.T) {
	manager := newLoyaltyTestManager(t)
	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)

	minEmission := tokensToWei(10)
	cfg := (&loyalty.GlobalConfig{
		Active:   true,
		Treasury: make([]byte, 20),
		Dynamic: loyalty.DynamicConfig{
			DailyCapPctOf7dFeesBps: 0,
			DailyCapUsd:            0,
			PriceGuard: loyalty.PriceGuardConfig{
				Enabled:                 true,
				PricePair:               "ZNHB/USD",
				TwapWindowSeconds:       60,
				PriceMaxAgeSeconds:      60,
				FallbackMinEmissionZNHB: new(big.Int).Set(minEmission),
			},
		},
	}).Normalize()
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		t.Fatalf("set loyalty config: %v", err)
	}

	budget, fallback, err := manager.GetRemainingDailyBudgetZNHB(now)
	if err != nil {
		t.Fatalf("remaining budget: %v", err)
	}
	if budget.Cmp(minEmission) != 0 {
		t.Fatalf("unexpected budget: got %s want %s", budget.String(), minEmission.String())
	}
	if fallback == nil {
		t.Fatalf("expected fallback signal when only min emission is configured")
	}
	if fallback.Strategy != "min_emission" {
		t.Fatalf("unexpected fallback strategy: %s", fallback.Strategy)
	}
	if fallback.BudgetZNHB.Cmp(budget) != 0 {
		t.Fatalf("expected fallback budget to match remaining budget")
	}
}
