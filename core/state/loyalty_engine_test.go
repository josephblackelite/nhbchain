package state

import (
	"testing"

	"nhbchain/native/loyalty"
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
