package gov_test

import (
	"errors"
	"testing"

	"nhbchain/config"
	"nhbchain/native/fees"
	govcfg "nhbchain/native/gov"
)

func testBaseline() govcfg.Baseline {
	return govcfg.Baseline{
		Governance: govcfg.GovernanceBaseline{
			QuorumBPS:        6000,
			PassThresholdBPS: 5000,
			VotingPeriodSecs: config.MinVotingPeriodSeconds,
		},
		Slashing: govcfg.SlashingBaseline{
			MinWindowSecs: 60,
			MaxWindowSecs: 600,
		},
		Mempool: govcfg.MempoolBaseline{MaxBytes: 1},
		Blocks:  govcfg.BlocksBaseline{MaxTxs: 1},
		Fees: govcfg.FeesBaseline{
			FreeTierTxPerMonth: config.DefaultFreeTierTxPerMonth,
			MDRBasisPoints:     config.DefaultMDRBasisPoints,
			Assets: []govcfg.FeeAssetBaseline{
				{Asset: fees.AssetNHB, MDRBasisPoints: config.DefaultMDRBasisPoints},
				{Asset: fees.AssetZNHB, MDRBasisPoints: config.DefaultMDRBasisPoints},
			},
		},
	}
}

func TestPreflightBaselineApplyRejectsInvalidPolicies(t *testing.T) {
	baseline := testBaseline()

	invalidQuorum := govcfg.PolicyDelta{Governance: &govcfg.GovernanceDelta{QuorumBPS: uint32Ptr(4000)}}
	if err := govcfg.PreflightBaselineApply(baseline, invalidQuorum); !errors.Is(err, govcfg.ErrInvalidPolicyInvariants) {
		t.Fatalf("expected quorum invariant error, got %v", err)
	}

	zeroPeriod := uint64(0)
	invalidPeriod := govcfg.PolicyDelta{Governance: &govcfg.GovernanceDelta{VotingPeriodSecs: &zeroPeriod}}
	if err := govcfg.PreflightBaselineApply(baseline, invalidPeriod); !errors.Is(err, govcfg.ErrInvalidPolicyInvariants) {
		t.Fatalf("expected voting period invariant error, got %v", err)
	}

	zeroWindow := uint64(0)
	invalidWindow := govcfg.PolicyDelta{Slashing: &govcfg.SlashingDelta{MinWindowSecs: &zeroWindow}}
	if err := govcfg.PreflightBaselineApply(baseline, invalidWindow); !errors.Is(err, govcfg.ErrInvalidPolicyInvariants) {
		t.Fatalf("expected slashing min invariant error, got %v", err)
	}

	minWindow := uint64(700)
	invalidRange := govcfg.PolicyDelta{Slashing: &govcfg.SlashingDelta{MinWindowSecs: &minWindow}}
	maxWindow := uint64(650)
	invalidRange.Slashing.MaxWindowSecs = &maxWindow
	if err := govcfg.PreflightBaselineApply(baseline, invalidRange); !errors.Is(err, govcfg.ErrInvalidPolicyInvariants) {
		t.Fatalf("expected slashing range invariant error, got %v", err)
	}
}

func TestPreflightBaselineApplyAllowsValidPolicy(t *testing.T) {
	baseline := testBaseline()
	quorum := uint32(6500)
	threshold := uint32(5200)
	voting := config.MinVotingPeriodSeconds + 3600
	minWindow := uint64(120)
	maxWindow := uint64(3600)
	delta := govcfg.PolicyDelta{
		Governance: &govcfg.GovernanceDelta{
			QuorumBPS:        &quorum,
			PassThresholdBPS: &threshold,
			VotingPeriodSecs: &voting,
		},
		Slashing: &govcfg.SlashingDelta{
			MinWindowSecs: &minWindow,
			MaxWindowSecs: &maxWindow,
		},
	}
	if err := govcfg.PreflightBaselineApply(baseline, delta); err != nil {
		t.Fatalf("expected valid policy delta, got %v", err)
	}
}

func uint32Ptr(v uint32) *uint32 { return &v }
