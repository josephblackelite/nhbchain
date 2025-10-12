package state

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"nhbchain/native/loyalty"
)

const (
	loyaltyDayFormat = "20060102"
)

// PendingRewards tracks the set of loyalty rewards awaiting settlement.
type PendingRewards []PendingReward

// AddPendingReward appends the supplied reward to the queue while defensively
// copying any mutable fields.
func (p *PendingRewards) AddPendingReward(reward PendingReward) {
	if p == nil {
		return
	}
	if reward.AmountZNHB == nil || reward.AmountZNHB.Sign() <= 0 {
		return
	}
	copied := reward
	copied.AmountZNHB = new(big.Int).Set(reward.AmountZNHB)
	*p = append(*p, copied)
}

// ClearPendingRewards empties the pending reward queue in-place.
func (p *PendingRewards) ClearPendingRewards() {
	if p == nil {
		return
	}
	*p = (*p)[:0]
}

// SumPending returns the aggregate ZNHB amount across all queued rewards.
func (p PendingRewards) SumPending() *big.Int {
	total := big.NewInt(0)
	for i := range p {
		if p[i].AmountZNHB == nil {
			continue
		}
		total.Add(total, p[i].AmountZNHB)
	}
	return total
}

type loyaltyDaySnapshot struct {
	PaidZNHB          *big.Int
	TotalProposedZNHB *big.Int
}

func newLoyaltyDaySnapshot() *loyaltyDaySnapshot {
	return (&loyaltyDaySnapshot{}).normalize()
}

func (s *loyaltyDaySnapshot) normalize() *loyaltyDaySnapshot {
	if s == nil {
		return &loyaltyDaySnapshot{PaidZNHB: big.NewInt(0), TotalProposedZNHB: big.NewInt(0)}
	}
	if s.PaidZNHB == nil {
		s.PaidZNHB = big.NewInt(0)
	}
	if s.PaidZNHB.Sign() < 0 {
		s.PaidZNHB = big.NewInt(0)
	}
	if s.TotalProposedZNHB == nil {
		s.TotalProposedZNHB = big.NewInt(0)
	}
	if s.TotalProposedZNHB.Sign() < 0 {
		s.TotalProposedZNHB = big.NewInt(0)
	}
	return s
}

func loyaltyDayKey(day string) []byte {
	key := make([]byte, len(loyaltyDayPrefix)+len(day))
	copy(key, loyaltyDayPrefix)
	copy(key[len(loyaltyDayPrefix):], day)
	return key
}

func loyaltyDayFromTime(ts time.Time) string {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return ts.UTC().Format(loyaltyDayFormat)
}

func (m *Manager) loadLoyaltyDaySnapshot(day string) (*loyaltyDaySnapshot, error) {
	if m == nil {
		return nil, fmt.Errorf("loyalty day snapshot: state manager unavailable")
	}
	trimmed := strings.TrimSpace(day)
	if trimmed == "" {
		trimmed = loyaltyDayFromTime(time.Now())
	}
	var stored loyaltyDaySnapshot
	ok, err := m.KVGet(loyaltyDayKey(trimmed), &stored)
	if err != nil {
		return nil, err
	}
	if !ok {
		return newLoyaltyDaySnapshot(), nil
	}
	return stored.normalize(), nil
}

func (m *Manager) storeLoyaltyDaySnapshot(day string, snapshot *loyaltyDaySnapshot) error {
	if m == nil {
		return fmt.Errorf("loyalty day snapshot: state manager unavailable")
	}
	if snapshot == nil {
		snapshot = newLoyaltyDaySnapshot()
	}
	trimmed := strings.TrimSpace(day)
	if trimmed == "" {
		trimmed = loyaltyDayFromTime(time.Now())
	}
	return m.KVPut(loyaltyDayKey(trimmed), snapshot.normalize())
}

// AddProposedTodayZNHB increments the running total of proposed base rewards
// for the supplied UTC day. The updated total is returned.
func (m *Manager) AddProposedTodayZNHB(now time.Time, amount *big.Int) (*big.Int, error) {
	if m == nil {
		return big.NewInt(0), fmt.Errorf("loyalty proposed: state manager unavailable")
	}
	if amount == nil || amount.Sign() <= 0 {
		snapshot, err := m.loadLoyaltyDaySnapshot(loyaltyDayFromTime(now))
		if err != nil {
			return nil, err
		}
		return new(big.Int).Set(snapshot.TotalProposedZNHB), nil
	}
	day := loyaltyDayFromTime(now)
	snapshot, err := m.loadLoyaltyDaySnapshot(day)
	if err != nil {
		return nil, err
	}
	snapshot.TotalProposedZNHB = new(big.Int).Add(snapshot.TotalProposedZNHB, amount)
	if err := m.storeLoyaltyDaySnapshot(day, snapshot); err != nil {
		return nil, err
	}
	return new(big.Int).Set(snapshot.TotalProposedZNHB), nil
}

// AddPaidTodayZNHB increments the paid total for the supplied UTC day and
// returns the new cumulative sum.
func (m *Manager) AddPaidTodayZNHB(now time.Time, amount *big.Int) (*big.Int, error) {
	if m == nil {
		return big.NewInt(0), fmt.Errorf("loyalty paid: state manager unavailable")
	}
	if amount == nil || amount.Sign() <= 0 {
		snapshot, err := m.loadLoyaltyDaySnapshot(loyaltyDayFromTime(now))
		if err != nil {
			return nil, err
		}
		return new(big.Int).Set(snapshot.PaidZNHB), nil
	}
	day := loyaltyDayFromTime(now)
	snapshot, err := m.loadLoyaltyDaySnapshot(day)
	if err != nil {
		return nil, err
	}
	snapshot.PaidZNHB = new(big.Int).Add(snapshot.PaidZNHB, amount)
	if err := m.storeLoyaltyDaySnapshot(day, snapshot); err != nil {
		return nil, err
	}
	return new(big.Int).Set(snapshot.PaidZNHB), nil
}

// GetRemainingDailyBudgetZNHB resolves the remaining daily base reward budget
// expressed in ZNHB for the supplied timestamp. When price guard checks fail or
// configuration is missing the function returns zero without error.
func (m *Manager) GetRemainingDailyBudgetZNHB(now time.Time) (*big.Int, error) {
	if m == nil {
		return big.NewInt(0), fmt.Errorf("loyalty budget: state manager unavailable")
	}
	cfg, err := m.LoyaltyGlobalConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return big.NewInt(0), nil
	}
	normalized := cfg.Clone().Normalize()
	day := loyaltyDayFromTime(now)
	snapshot, err := m.loadLoyaltyDaySnapshot(day)
	if err != nil {
		return nil, err
	}
	price, ok, err := m.resolveLoyaltyPrice(now, normalized.Dynamic.PriceGuard)
	if err != nil {
		return nil, err
	}
	if !ok {
		return big.NewInt(0), nil
	}

	tracker := NewRollingFees(m)
	feesNHB, err := tracker.Get7dNetFeesNHB(now)
	if err != nil {
		return nil, err
	}
	feesZNHB, err := tracker.Get7dNetFeesZNHB(now)
	if err != nil {
		return nil, err
	}

	budget := CalcDailyBudgetZNHB(now, feesNHB, feesZNHB, price, &normalized.Dynamic)
	remaining := new(big.Int).Sub(budget, snapshot.PaidZNHB)
	if remaining.Sign() < 0 {
		remaining = big.NewInt(0)
	}
	return remaining, nil
}

func (m *Manager) resolveLoyaltyPrice(now time.Time, guard loyalty.PriceGuardConfig) (*big.Rat, bool, error) {
	normalized := guard
	normalized.Normalize()
	if !normalized.Enabled {
		return big.NewRat(1, 1), true, nil
	}
	pair := strings.TrimSpace(normalized.PricePair)
	parts := strings.Split(pair, "/")
	base := "ZNHB"
	if len(parts) > 0 {
		token := strings.TrimSpace(parts[0])
		if token != "" {
			base = strings.ToUpper(token)
		}
	}
	record, ok, err := m.SwapLastPriceProof(base)
	if err != nil {
		return nil, false, err
	}
	if !ok || record == nil || record.Rate == nil || record.Rate.Sign() <= 0 {
		return nil, false, nil
	}
	if normalized.PriceMaxAgeSeconds > 0 {
		cutoff := now.UTC().Add(-time.Duration(normalized.PriceMaxAgeSeconds) * time.Second)
		if record.Timestamp.IsZero() || record.Timestamp.Before(cutoff) {
			return nil, false, nil
		}
	}
	return new(big.Rat).Set(record.Rate), true, nil
}

// PendingReward captures a loyalty reward that has been computed during the
// block but not yet minted. Rewards are accumulated and settled once the block
// successfully completes.
type PendingReward struct {
	TxHash     [32]byte
	Payer      [20]byte
	Recipient  [20]byte
	AmountZNHB *big.Int
}

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
	YtdEmissionsZNHB *big.Int
	YearlyCapZNHB    *big.Int
}

// Clone returns a deep copy of the dynamic state.
func (s *LoyaltyEngineState) Clone() *LoyaltyEngineState {
	if s == nil {
		return nil
	}
	clone := *s
	if s.YtdEmissionsZNHB != nil {
		clone.YtdEmissionsZNHB = new(big.Int).Set(s.YtdEmissionsZNHB)
	}
	if s.YearlyCapZNHB != nil {
		clone.YearlyCapZNHB = new(big.Int).Set(s.YearlyCapZNHB)
	}
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
	if s.YtdEmissionsZNHB == nil {
		s.YtdEmissionsZNHB = big.NewInt(0)
	}
	if s.YearlyCapZNHB == nil {
		s.YearlyCapZNHB = big.NewInt(0)
	}
	if s.YtdEmissionsZNHB.Sign() < 0 {
		s.YtdEmissionsZNHB = big.NewInt(0)
	}
	if s.YearlyCapZNHB.Sign() < 0 {
		s.YearlyCapZNHB = big.NewInt(0)
	}
	if s.YearlyCapZNHB.Sign() > 0 && s.YtdEmissionsZNHB.Cmp(s.YearlyCapZNHB) > 0 {
		s.YtdEmissionsZNHB = new(big.Int).Set(s.YearlyCapZNHB)
	}
	return s
}

// CanEmit determines whether an additional emission can be produced without
// breaching the configured yearly cap. When the emission is permitted the YTD
// tally is incremented. The returned boolean indicates whether the emission is
// allowed, while the second boolean reports whether the cap has been hit (i.e.
// the YTD total equals the cap after processing). Callers should treat negative
// or nil amounts as no-ops that always succeed.
func (s *LoyaltyEngineState) CanEmit(amount *big.Int) (bool, bool) {
	if s == nil {
		return false, false
	}
	s.Normalize()
	if amount == nil || amount.Sign() <= 0 {
		return true, s.YearlyCapZNHB.Sign() > 0 && s.YtdEmissionsZNHB.Cmp(s.YearlyCapZNHB) == 0
	}
	if s.YearlyCapZNHB.Sign() <= 0 {
		s.YtdEmissionsZNHB = new(big.Int).Add(s.YtdEmissionsZNHB, amount)
		return true, false
	}
	projected := new(big.Int).Add(s.YtdEmissionsZNHB, amount)
	cmp := projected.Cmp(s.YearlyCapZNHB)
	if cmp > 0 {
		return false, true
	}
	s.YtdEmissionsZNHB = projected
	return true, cmp == 0
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
		YtdEmissionsZNHB: big.NewInt(0),
		YearlyCapZNHB:    big.NewInt(0),
	}
	return state.Normalize()
}
