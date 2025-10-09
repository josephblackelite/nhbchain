package gov

import (
	"errors"
	"testing"

	"nhbchain/config"
)

func TestPreflightPolicyApplyRejectsInvalidQuorum(t *testing.T) {
	cfg := config.Global{
		Governance: config.Governance{QuorumBPS: 6000, PassThresholdBPS: 5000, VotingPeriodSecs: 7200},
		Slashing:   config.Slashing{MinWindowSecs: 60, MaxWindowSecs: 600},
		Mempool:    config.Mempool{MaxBytes: 1024},
		Blocks:     config.Blocks{MaxTxs: 64},
		Staking:    validStakingConfig(),
	}
	newQuorum := uint32(4000)
	err := PreflightPolicyApply(cfg, PolicyDelta{Governance: &GovernanceDelta{QuorumBPS: &newQuorum}})
	if err == nil {
		t.Fatalf("expected quorum < threshold rejection")
	}
	if !errors.Is(err, ErrInvalidPolicyInvariants) {
		t.Fatalf("expected ErrInvalidPolicyInvariants, got %v", err)
	}
}

func TestPreflightPolicyApplyAllowsValidChange(t *testing.T) {
	cfg := config.Global{
		Governance: config.Governance{QuorumBPS: 6000, PassThresholdBPS: 5000, VotingPeriodSecs: 7200},
		Slashing:   config.Slashing{MinWindowSecs: 60, MaxWindowSecs: 600},
		Mempool:    config.Mempool{MaxBytes: 1024},
		Blocks:     config.Blocks{MaxTxs: 64},
		Staking:    validStakingConfig(),
	}
	newQuorum := uint32(6000)
	newThreshold := uint32(5500)
	delta := PolicyDelta{Governance: &GovernanceDelta{QuorumBPS: &newQuorum, PassThresholdBPS: &newThreshold}}
	if err := PreflightPolicyApply(cfg, delta); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func validStakingConfig() config.Staking {
	return config.Staking{
		AprBps:                1250,
		PayoutPeriodDays:      30,
		UnbondingDays:         7,
		MinStakeWei:           "0",
		MaxEmissionPerYearWei: "0",
		RewardAsset:           "ZNHB",
	}
}
