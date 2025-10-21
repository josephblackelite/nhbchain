package config

import (
	"math"
	"math/big"
	"strings"
	"time"
)

const (
	DefaultFreeTierTxPerMonth = uint64(100)
	DefaultMDRBasisPoints     = uint32(150)
)

// RPCProxyHeaders configures reverse proxy header handling for the public RPC endpoint.
type RPCProxyHeaders struct {
	XForwardedFor string `toml:"XForwardedFor"`
	XRealIP       string `toml:"XRealIP"`
}

// RPCJWT captures JWT validation settings enforced by the RPC server.
type RPCJWT struct {
	Enable           bool     `toml:"Enable"`
	Alg              string   `toml:"Alg"`
	HSSecretEnv      string   `toml:"HSSecretEnv"`
	RSAPublicKeyFile string   `toml:"RSAPublicKeyFile"`
	Issuer           string   `toml:"Issuer"`
	Audience         []string `toml:"Audience"`
	MaxSkewSeconds   int64    `toml:"MaxSkewSeconds"`
}

// RPCSwapAuthPersistence configures the backend used for durable swap nonce storage.
type RPCSwapAuthPersistence struct {
	Backend     string `toml:"Backend"`
	LevelDBPath string `toml:"LevelDBPath"`
}

// RPCSwapAuth configures HMAC authentication for swap RPC methods.
type RPCSwapAuth struct {
	Secrets                  map[string]string      `toml:"Secrets"`
	AllowedTimestampSkewSecs int                    `toml:"AllowedTimestampSkewSeconds"`
	NonceTTLSeconds          int                    `toml:"NonceTTLSeconds"`
	NonceCapacity            int                    `toml:"NonceCapacity"`
	RateLimitWindowSeconds   int                    `toml:"RateLimitWindowSeconds"`
	PartnerRateLimits        map[string]int         `toml:"PartnerRateLimits"`
	Persistence              RPCSwapAuthPersistence `toml:"Persistence"`
}

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

// Staking captures the runtime configuration for validator and delegator rewards.
type Staking struct {
	AprBps                uint32
	PayoutPeriodDays      uint32
	UnbondingDays         uint32
	MinStakeWei           string
	MaxEmissionPerYearWei string
	RewardAsset           string
	CompoundDefault       bool
}

type Pauses struct {
	Lending      bool
	Swap         bool
	Escrow       bool
	Trade        bool
	Loyalty      bool
	POTSO        bool
	TransferNHB  bool
	TransferZNHB bool
	Staking      bool
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

// Loyalty controls the automatic adjustments applied to the base loyalty reward rate.
type Loyalty struct {
	Dynamic LoyaltyDynamic
}

// LoyaltyDynamic captures the guardrails enforced by the adaptive loyalty controller.
type LoyaltyDynamic struct {
	TargetBPS                   uint32
	MinBPS                      uint32
	MaxBPS                      uint32
	SmoothingStepBPS            uint32
	CoverageMax                 float64
	CoverageLookbackDays        uint32
	DailyCapPctOf7dFees         float64
	DailyCapUSD                 float64
	YearlyCapPctOfInitialSupply float64
	PriceGuard                  LoyaltyPriceGuard
	EnableProRate               bool
	EnforceProRate              bool
}

// YearlyCapZNHBWei converts the configured annual issuance percentage into an
// absolute ZNHB amount expressed in wei based on the supplied initial supply.
// When the percentage or the initial supply are unset (zero or negative) the
// function returns zero.
func (d LoyaltyDynamic) YearlyCapZNHBWei(initialSupply *big.Int) *big.Int {
	if initialSupply == nil || initialSupply.Sign() <= 0 {
		return big.NewInt(0)
	}
	pctBps := int64(math.Round(d.YearlyCapPctOfInitialSupply * 100))
	if pctBps <= 0 {
		return big.NewInt(0)
	}
	numerator := new(big.Int).Mul(initialSupply, big.NewInt(pctBps))
	denominator := big.NewInt(10_000)
	numerator.Quo(numerator, denominator)
	if numerator.Sign() < 0 {
		return big.NewInt(0)
	}
	return numerator
}

// LoyaltyPriceGuard defines the deviation limits applied when consuming external price data.
type LoyaltyPriceGuard struct {
	Enabled                    bool
	PricePair                  string
	TwapWindowSeconds          uint32
	MaxDeviationBPS            uint32
	PriceMaxAgeSeconds         uint32
	FallbackMinEmissionZNHBWei string
	UseLastGoodPriceFallback   bool
}

// Global bundles the runtime configuration values enforced by ValidateConfig.
type Global struct {
	Governance Governance
	Slashing   Slashing
	Mempool    Mempool
	Blocks     Blocks
	Staking    Staking
	Pauses     Pauses
	Quotas     Quotas
	Paymaster  Paymaster
	Fees       Fees
	Loyalty    Loyalty
}
