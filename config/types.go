package config

import "time"

// Governance captures global governance policy knobs that must be validated
// before applying runtime configuration updates.
type Governance struct {
	QuorumBPS        uint32
	PassThresholdBPS uint32
	VotingPeriodSecs uint64
}

// Slashing defines the allowed window bounds for penalty evaluation.
type Slashing struct {
	MinWindowSecs uint64
	MaxWindowSecs uint64
}

// Mempool controls global transaction admission limits.
type Mempool struct {
	MaxBytes int64
}

// Blocks captures block production limits for transaction counts.
type Blocks struct {
	MaxTxs int64
}

// Consensus controls the BFT round timeouts.
type Consensus struct {
	ProposalTimeout  time.Duration `toml:"ProposalTimeout"`
	PrevoteTimeout   time.Duration `toml:"PrevoteTimeout"`
	PrecommitTimeout time.Duration `toml:"PrecommitTimeout"`
	CommitTimeout    time.Duration `toml:"CommitTimeout"`
}

type Pauses struct {
	Lending bool
	Swap    bool
	Escrow  bool
	Trade   bool
	Loyalty bool
	POTSO   bool
}

// Quota defines rate limits for module interactions on a per-address basis.
type Quota struct {
	MaxRequestsPerMin uint32
	MaxNHBPerEpoch    uint64 // in gwei or base units
	EpochSeconds      uint32 // e.g., 3600
}

// Quotas groups quotas for each module.
type Quotas struct {
	Lending Quota
	Swap    Quota
	Escrow  Quota
	Trade   Quota
	Loyalty Quota
	POTSO   Quota
}

// Global bundles the runtime configuration values enforced by ValidateConfig.
type Global struct {
	Governance Governance
	Slashing   Slashing
	Mempool    Mempool
	Blocks     Blocks
	Pauses     Pauses
	Quotas     Quotas
}
