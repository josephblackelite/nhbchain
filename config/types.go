package config

import (
	"strings"
	"time"
)

const (
	DefaultFreeTierTxPerMonth = uint64(100)
	DefaultMDRBasisPoints     = uint32(150)
)

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
	MaxBytes          int64
	POSReservationBPS uint32
}

// Blocks captures block production limits for transaction counts.
type Blocks struct {
	MaxTxs int64
}

// Paymaster captures sponsorship throttling configuration knobs.
type Paymaster struct {
	MerchantDailyCapWei string
	DeviceDailyTxCap    uint64
	GlobalDailyCapWei   string
	AutoTopUp           PaymasterAutoTopUp
}

// PaymasterAutoTopUp configures the automatic paymaster replenishment policy.
type PaymasterAutoTopUp struct {
	Enabled         bool
	Token           string
	MinBalanceWei   string
	TopUpAmountWei  string
	DailyCapWei     string
	CooldownSeconds uint64
	Governance      PaymasterAutoTopUpGovernance
}

// PaymasterAutoTopUpGovernance captures the role based guardrails required to
// execute automatic top-ups.
type PaymasterAutoTopUpGovernance struct {
	Operator     string
	MinterRole   string
	ApproverRole string
}

// FeeAsset captures per-asset MDR and routing configuration.
type FeeAsset struct {
	Asset          string
	MDRBasisPoints uint32
	OwnerWallet    string
}

// Fees captures default fee policy settings applied across domains.
type Fees struct {
	FreeTierTxPerMonth uint64
	MDRBasisPoints     uint32
	OwnerWallet        string
	Assets             []FeeAsset
}

// RouteWalletByAsset returns a normalised map of asset identifiers to the
// configured route wallet. Empty wallet entries are omitted.
func (f Fees) RouteWalletByAsset() map[string]string {
	if len(f.Assets) == 0 {
		return map[string]string{}
	}
	wallets := make(map[string]string, len(f.Assets))
	for _, asset := range f.Assets {
		name := strings.ToUpper(strings.TrimSpace(asset.Asset))
		if name == "" {
			continue
		}
		wallet := strings.TrimSpace(asset.OwnerWallet)
		if wallet == "" {
			continue
		}
		wallets[name] = wallet
	}
	return wallets
}

// Consensus controls the BFT round timeouts.
type Consensus struct {
	ProposalTimeout  time.Duration `toml:"ProposalTimeout"`
	PrevoteTimeout   time.Duration `toml:"PrevoteTimeout"`
	PrecommitTimeout time.Duration `toml:"PrecommitTimeout"`
	CommitTimeout    time.Duration `toml:"CommitTimeout"`
}

type Pauses struct {
	Lending      bool
	Swap         bool
	Escrow       bool
	Trade        bool
	Loyalty      bool
	POTSO        bool
	TransferZNHB bool
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
	Paymaster  Paymaster
	Fees       Fees
}
