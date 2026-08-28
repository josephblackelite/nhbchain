package core

import (
	"encoding/json"
	"fmt"
	"strings"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/governance"
	"nhbchain/native/lending"
)

// effectiveFixedTermRateSchedule resolves the fixed-term lending tenure->rate
// table entirely from the governance param store, falling back to
// native/lending.DefaultFixedTermRateSchedule when no
// policy.lendingRateSchedule proposal has ever passed. Read fresh from state
// on every call (see lendingEngine() in core/lending_native.go, called for
// every fixed-term borrow) so a passed proposal takes effect on the very
// next transaction, network-wide, with no node restart -- mirrors
// core/swap_risk_params.go's effectiveRedeemRiskParameters precedent.
func (sp *StateProcessor) effectiveFixedTermRateSchedule(manager *nhbstate.Manager) (lending.TenureRateSchedule, error) {
	raw, ok, err := manager.ParamStoreGet(governance.ParamKeyLendingFixedTermRateSchedule)
	if err != nil {
		return nil, err
	}
	if !ok {
		return cloneFixedTermRateSchedule(lending.DefaultFixedTermRateSchedule), nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return cloneFixedTermRateSchedule(lending.DefaultFixedTermRateSchedule), nil
	}

	var payload governance.LendingRateSchedulePayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("lending rate schedule: invalid stored value: %w", err)
	}
	if len(payload.Schedule) == 0 {
		return nil, fmt.Errorf("lending rate schedule: stored value has no entries")
	}
	schedule := make(lending.TenureRateSchedule, len(payload.Schedule))
	for _, entry := range payload.Schedule {
		if entry.TenureDays == 0 || entry.RateBps == 0 {
			return nil, fmt.Errorf("lending rate schedule: stored entry has a zero tenureDays or rateBps")
		}
		schedule[entry.TenureDays] = entry.RateBps
	}
	return schedule, nil
}

func cloneFixedTermRateSchedule(schedule lending.TenureRateSchedule) lending.TenureRateSchedule {
	clone := make(lending.TenureRateSchedule, len(schedule))
	for tenureDays, rateBps := range schedule {
		clone[tenureDays] = rateBps
	}
	return clone
}

// effectiveFixedTermDepositRateSchedule mirrors effectiveFixedTermRateSchedule
// exactly, for the DEPOSIT (Milestone 3) side. Unlike the borrow side, there
// is no built-in default schedule to fall back to -- until a
// policy.lendingDepositRateSchedule proposal has ever executed, NO tenure is
// deposit-eligible (an empty schedule), since there is no safe default
// deposit rate to assume without a governance decision setting one.
func (sp *StateProcessor) effectiveFixedTermDepositRateSchedule(manager *nhbstate.Manager) (lending.TenureRateSchedule, error) {
	raw, ok, err := manager.ParamStoreGet(governance.ParamKeyLendingFixedTermDepositRateSchedule)
	if err != nil {
		return nil, err
	}
	if !ok {
		return lending.TenureRateSchedule{}, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return lending.TenureRateSchedule{}, nil
	}

	var payload governance.LendingRateSchedulePayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("lending deposit rate schedule: invalid stored value: %w", err)
	}
	if len(payload.Schedule) == 0 {
		return nil, fmt.Errorf("lending deposit rate schedule: stored value has no entries")
	}
	schedule := make(lending.TenureRateSchedule, len(payload.Schedule))
	for _, entry := range payload.Schedule {
		if entry.TenureDays == 0 || entry.RateBps == 0 {
			return nil, fmt.Errorf("lending deposit rate schedule: stored entry has a zero tenureDays or rateBps")
		}
		schedule[entry.TenureDays] = entry.RateBps
	}
	return schedule, nil
}
