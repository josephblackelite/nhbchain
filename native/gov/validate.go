package gov

import (
	"errors"
	"fmt"

	"nhbchain/config"
)

// ErrInvalidPolicyInvariants identifies proposals whose resulting configuration
// would violate global invariants enforced by the runtime.
var ErrInvalidPolicyInvariants = errors.New("governance: invalid policy invariants")

// PolicyDelta captures the subset of global configuration fields that a policy
// proposal may mutate. Fields are optional so callers can supply sparse deltas
// without specifying every configuration knob.
type PolicyDelta struct {
	Governance *GovernanceDelta
	Slashing   *SlashingDelta
	Mempool    *MempoolDelta
	Blocks     *BlocksDelta
}

// Baseline represents the current global configuration using primitive values
// so callers outside the config package can provide a snapshot without
// introducing import cycles.
type Baseline struct {
	Governance GovernanceBaseline
	Slashing   SlashingBaseline
	Mempool    MempoolBaseline
	Blocks     BlocksBaseline
	Fees       FeesBaseline
}

// GovernanceBaseline mirrors config.Governance using primitive types.
type GovernanceBaseline struct {
	QuorumBPS        uint32
	PassThresholdBPS uint32
	VotingPeriodSecs uint64
}

// SlashingBaseline mirrors config.Slashing using primitive types.
type SlashingBaseline struct {
	MinWindowSecs uint64
	MaxWindowSecs uint64
}

// MempoolBaseline mirrors config.Mempool using primitive types.
type MempoolBaseline struct {
	MaxBytes int64
}

// BlocksBaseline mirrors config.Blocks using primitive types.
type BlocksBaseline struct {
	MaxTxs int64
}

// FeesBaseline mirrors config.Fees using primitive types.
type FeesBaseline struct {
	FreeTierTxPerMonth uint64
	MDRBasisPoints     uint32
	OwnerWallet        string
}

// GovernanceDelta represents proposed changes to governance thresholds and
// timing parameters.
type GovernanceDelta struct {
	QuorumBPS        *uint32
	PassThresholdBPS *uint32
	VotingPeriodSecs *uint64
}

// SlashingDelta captures proposed changes to slashing window bounds.
type SlashingDelta struct {
	MinWindowSecs *uint64
	MaxWindowSecs *uint64
}

// MempoolDelta represents mempool sizing adjustments.
type MempoolDelta struct {
	MaxBytes *int64
}

// BlocksDelta represents block sizing adjustments.
type BlocksDelta struct {
	MaxTxs *int64
}

// PreflightPolicyApply merges the proposed delta over the supplied baseline
// configuration and validates that the resulting configuration satisfies all
// global invariants. The returned error is wrapped with ErrInvalidPolicyInvariants
// to allow callers to map rejections to structured error codes.
func PreflightPolicyApply(cur config.Global, delta PolicyDelta) error {
	cand := applyDelta(cur, delta)
	if err := config.ValidateConfig(cand); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPolicyInvariants, err)
	}
	return nil
}

// PreflightBaselineApply constructs a config.Global from the supplied snapshot
// and validates that applying the delta preserves all invariants.
func PreflightBaselineApply(cur Baseline, delta PolicyDelta) error {
	return PreflightPolicyApply(config.Global{
		Governance: config.Governance{
			QuorumBPS:        cur.Governance.QuorumBPS,
			PassThresholdBPS: cur.Governance.PassThresholdBPS,
			VotingPeriodSecs: cur.Governance.VotingPeriodSecs,
		},
		Slashing: config.Slashing{
			MinWindowSecs: cur.Slashing.MinWindowSecs,
			MaxWindowSecs: cur.Slashing.MaxWindowSecs,
		},
		Mempool: config.Mempool{MaxBytes: cur.Mempool.MaxBytes},
		Blocks:  config.Blocks{MaxTxs: cur.Blocks.MaxTxs},
		Fees: config.Fees{
			FreeTierTxPerMonth: cur.Fees.FreeTierTxPerMonth,
			MDRBasisPoints:     cur.Fees.MDRBasisPoints,
			OwnerWallet:        cur.Fees.OwnerWallet,
		},
	}, delta)
}

func applyDelta(cur config.Global, delta PolicyDelta) config.Global {
	candidate := cur
	if delta.Governance != nil {
		if delta.Governance.QuorumBPS != nil {
			candidate.Governance.QuorumBPS = *delta.Governance.QuorumBPS
		}
		if delta.Governance.PassThresholdBPS != nil {
			candidate.Governance.PassThresholdBPS = *delta.Governance.PassThresholdBPS
		}
		if delta.Governance.VotingPeriodSecs != nil {
			candidate.Governance.VotingPeriodSecs = *delta.Governance.VotingPeriodSecs
		}
	}
	if delta.Slashing != nil {
		if delta.Slashing.MinWindowSecs != nil {
			candidate.Slashing.MinWindowSecs = *delta.Slashing.MinWindowSecs
		}
		if delta.Slashing.MaxWindowSecs != nil {
			candidate.Slashing.MaxWindowSecs = *delta.Slashing.MaxWindowSecs
		}
	}
	if delta.Mempool != nil {
		if delta.Mempool.MaxBytes != nil {
			candidate.Mempool.MaxBytes = *delta.Mempool.MaxBytes
		}
	}
	if delta.Blocks != nil {
		if delta.Blocks.MaxTxs != nil {
			candidate.Blocks.MaxTxs = *delta.Blocks.MaxTxs
		}
	}
	return candidate
}
