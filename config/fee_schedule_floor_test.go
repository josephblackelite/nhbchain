package config

import (
	"strings"
	"testing"

	"nhbchain/native/governance"
)

func feeFloorConfig(activation, delay uint64, nhb, znhb uint32) *Config {
	cfg := &Config{}
	cfg.TransferFeeGovernanceActivationHeight = activation
	cfg.TransferFeeGovernanceDelayBlocks = delay
	cfg.Global.Fees.TransferFeeBps = nhb
	cfg.Global.Fees.TransferFeeBpsZNHB = znhb
	return cfg
}

// With the rule off nothing is reported, whatever the other values are: a validator that does not
// use the schedule must keep starting.
func TestFeeScheduleProblemsAreQuietWhileTheRuleIsOff(t *testing.T) {
	if got := FeeScheduleProblems(feeFloorConfig(0, 0, 5000, 5000)); len(got) != 0 {
		t.Fatalf("rule off: %v", got)
	}
	if got := FeeScheduleProblems(nil); len(got) != 0 {
		t.Fatalf("nil config: %v", got)
	}
}

func TestFeeScheduleProblemsDelayFloor(t *testing.T) {
	floor := governance.MinFeeScheduleDelayBlocks
	for _, delay := range []uint64{0, 1, floor - 1} {
		got := FeeScheduleProblems(feeFloorConfig(2_700_000, delay, 20, 10))
		if len(got) != 1 || !strings.Contains(got[0].Error(), "TransferFeeGovernanceDelayBlocks") {
			t.Errorf("delay %d: problems %v, want one about the delay", delay, got)
		}
	}
	for _, delay := range []uint64{floor, floor + 1, 41_760} {
		if got := FeeScheduleProblems(feeFloorConfig(2_700_000, delay, 20, 10)); len(got) != 0 {
			t.Errorf("delay %d: problems %v, want none", delay, got)
		}
	}
}

func TestFeeScheduleProblemsConfiguredRateCeiling(t *testing.T) {
	ceiling := governance.MaxTransferFeeBps
	got := FeeScheduleProblems(feeFloorConfig(2_700_000, 41_760, ceiling+1, ceiling))
	if len(got) != 1 || !strings.Contains(got[0].Error(), "TransferFeeBps ") {
		t.Fatalf("NHB rate above the ceiling: %v", got)
	}
	got = FeeScheduleProblems(feeFloorConfig(2_700_000, 41_760, ceiling, ceiling+1))
	if len(got) != 1 || !strings.Contains(got[0].Error(), "TransferFeeBpsZNHB") {
		t.Fatalf("ZNHB rate above the ceiling: %v", got)
	}
	if got := FeeScheduleProblems(feeFloorConfig(2_700_000, 41_760, ceiling, ceiling)); len(got) != 0 {
		t.Fatalf("both at the ceiling: %v", got)
	}
}

// The refusal is opt-in, the same switch as the voting-window floors.
func TestEnforceFeeScheduleFloorsNeedsTheSwitch(t *testing.T) {
	cfg := feeFloorConfig(2_700_000, 5, 20, 10)
	if err := EnforceFeeScheduleFloors(cfg); err != nil {
		t.Fatalf("without EnforceWindowFloors a problem must not stop the node: %v", err)
	}
	cfg.Governance.EnforceWindowFloors = true
	err := EnforceFeeScheduleFloors(cfg)
	if err == nil || !strings.Contains(err.Error(), "TransferFeeGovernanceDelayBlocks 5") {
		t.Fatalf("with EnforceWindowFloors: %v, want a refusal naming the delay", err)
	}
	cfg.TransferFeeGovernanceDelayBlocks = 41_760
	if err := EnforceFeeScheduleFloors(cfg); err != nil {
		t.Fatalf("at a good value the node starts: %v", err)
	}
	cfg.TransferFeeGovernanceActivationHeight = 0
	cfg.TransferFeeGovernanceDelayBlocks = 0
	if err := EnforceFeeScheduleFloors(cfg); err != nil {
		t.Fatalf("with the rule off the node starts: %v", err)
	}
	if err := EnforceFeeScheduleFloors(nil); err != nil {
		t.Fatalf("nil config: %v", err)
	}
}
