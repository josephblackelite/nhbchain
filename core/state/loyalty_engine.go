package state

import "nhbchain/native/loyalty"

// LoyaltyEngineState captures the dynamic loyalty controller runtime state.
//
// All values are expressed in basis points and operate within the guardrails
// configured by governance.
type LoyaltyEngineState struct {
	EffectiveBps     uint32
	TargetBps        uint32
	MinBps           uint32
	MaxBps           uint32
	SmoothingStepBps uint32
}

// Clone returns a deep copy of the dynamic state.
func (s *LoyaltyEngineState) Clone() *LoyaltyEngineState {
	if s == nil {
		return nil
	}
	clone := *s
	return &clone
}

// Normalize enforces invariants on the dynamic state ensuring all fields lie
// within valid bounds. The receiver is returned to allow fluent usage.
func (s *LoyaltyEngineState) Normalize() *LoyaltyEngineState {
	if s == nil {
		return nil
	}
	if s.MinBps > s.MaxBps {
		s.MinBps, s.MaxBps = s.MaxBps, s.MinBps
	}
	if s.SmoothingStepBps == 0 {
		s.SmoothingStepBps = 1
	}
	if s.TargetBps < s.MinBps {
		s.TargetBps = s.MinBps
	}
	if s.TargetBps > s.MaxBps {
		s.TargetBps = s.MaxBps
	}
	if s.EffectiveBps < s.MinBps {
		s.EffectiveBps = s.MinBps
	}
	if s.EffectiveBps > s.MaxBps {
		s.EffectiveBps = s.MaxBps
	}
	return s
}

// ApplyDynamicConfig updates the runtime state guardrails using the supplied
// configuration while keeping the current effective basis points when possible.
func (s *LoyaltyEngineState) ApplyDynamicConfig(cfg loyalty.DynamicConfig) *LoyaltyEngineState {
	if s == nil {
		return nil
	}
	normalized := cfg.Clone()
	normalized.Normalize()
	s.TargetBps = normalized.TargetBps
	s.MinBps = normalized.MinBps
	s.MaxBps = normalized.MaxBps
	s.SmoothingStepBps = normalized.SmoothingStepBps
	return s.Normalize()
}

// StepTowardsTarget adjusts the effective basis points towards the configured
// target while respecting the configured smoothing step and guardrails. The
// method returns true when the effective basis points changed.
func (s *LoyaltyEngineState) StepTowardsTarget() bool {
	if s == nil {
		return false
	}
	s.Normalize()
	prev := s.EffectiveBps
	step := s.SmoothingStepBps
	if step == 0 {
		step = 1
	}
	if s.EffectiveBps < s.TargetBps {
		delta := s.TargetBps - s.EffectiveBps
		if delta > step {
			s.EffectiveBps += step
		} else {
			s.EffectiveBps = s.TargetBps
		}
	} else if s.EffectiveBps > s.TargetBps {
		delta := s.EffectiveBps - s.TargetBps
		if delta > step {
			s.EffectiveBps -= step
		} else {
			s.EffectiveBps = s.TargetBps
		}
	}
	if s.EffectiveBps < s.MinBps {
		s.EffectiveBps = s.MinBps
	}
	if s.EffectiveBps > s.MaxBps {
		s.EffectiveBps = s.MaxBps
	}
	return s.EffectiveBps != prev
}

// NewLoyaltyEngineStateFromDynamic produces a runtime state initialised from
// the provided dynamic configuration. The effective basis points start at the
// configured target value.
func NewLoyaltyEngineStateFromDynamic(cfg loyalty.DynamicConfig) *LoyaltyEngineState {
	normalized := cfg.Clone()
	normalized.Normalize()
	state := &LoyaltyEngineState{
		EffectiveBps:     normalized.TargetBps,
		TargetBps:        normalized.TargetBps,
		MinBps:           normalized.MinBps,
		MaxBps:           normalized.MaxBps,
		SmoothingStepBps: normalized.SmoothingStepBps,
	}
	return state.Normalize()
}
