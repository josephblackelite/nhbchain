package swap_test

import (
	"errors"
	"math/big"
	"testing"
	"time"

	swap "nhbchain/native/swap"
)

type pauseStub struct {
	paused bool
}

func (p pauseStub) IsPaused(module string) bool {
	if !p.paused {
		return false
	}
	return module == "swap"
}

func TestOracleStale_Blocks(t *testing.T) {
	guard, err := swap.NewOracleGuardrails(time.Minute, 100)
	if err != nil {
		t.Fatalf("new guardrails: %v", err)
	}
	now := time.Unix(1700000000, 0).UTC()
	observed := now.Add(-2 * time.Minute)
	violation, err := swap.ValidateOracleFreshness(guard, now, observed)
	if !errors.Is(err, swap.ErrOracleStale) {
		t.Fatalf("expected ErrOracleStale, got %v", err)
	}
	if violation == nil || violation.Code != swap.RiskCodeOracleStale {
		t.Fatalf("unexpected violation: %+v", violation)
	}
}

func TestDeviation_Blocks(t *testing.T) {
	guard, err := swap.NewOracleGuardrails(time.Minute, 50)
	if err != nil {
		t.Fatalf("new guardrails: %v", err)
	}
	prev := big.NewRat(1, 1)
	curr := big.NewRat(103, 100) // 3% deviation
	violation, err := swap.ValidateOracleDeviation(guard, prev, curr)
	if !errors.Is(err, swap.ErrOracleDeviation) {
		t.Fatalf("expected ErrOracleDeviation, got %v", err)
	}
	if violation == nil || violation.Code != swap.RiskCodeOracleDeviation {
		t.Fatalf("unexpected violation: %+v", violation)
	}
}

func TestDailyCap_Blocks(t *testing.T) {
	caps := swap.CashOutParameters{
		AssetDailyCaps: map[swap.StableAsset]*big.Int{
			swap.StableAssetUSDC: big.NewInt(1000),
		},
		TierCaps: map[string]swap.CashOutTierLimits{
			"gold": {
				Tier:        "gold",
				DailyCapWei: big.NewInt(500),
			},
		},
	}
	settled := big.NewInt(400)
	pending := big.NewInt(50)
	requested := big.NewInt(60)
	violation, err := caps.ValidateDailyCashOut(swap.StableAssetUSDC, "gold", settled, pending, requested)
	if !errors.Is(err, swap.ErrCashOutTierCapExceeded) {
		t.Fatalf("expected ErrCashOutTierCapExceeded, got %v", err)
	}
	if violation == nil || violation.Code != swap.RiskCodeCashOutTierCap {
		t.Fatalf("unexpected violation: %+v", violation)
	}
}

func TestPause_Blocks(t *testing.T) {
	violation, err := swap.ValidateSwapActive(pauseStub{paused: true})
	if !errors.Is(err, swap.ErrSwapPaused) {
		t.Fatalf("expected ErrSwapPaused, got %v", err)
	}
	if violation == nil || violation.Code != swap.RiskCodeModulePaused {
		t.Fatalf("unexpected violation: %+v", violation)
	}
}
