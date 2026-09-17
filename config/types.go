package config

import (
	"math"
	"math/big"
	"strings"
	"time"

	"nhbchain/native/subscriptions"
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
	FundingAccount string
	Minter         string
	Approver       string
	MinterRole     string
	ApproverRole   string
}

// FeeAsset captures per-asset MDR and routing configuration.
type FeeAsset struct {
	Asset          string
	MDRBasisPoints uint32
	OwnerWallet    string
}

// Fees captures default fee policy settings applied across domains.
type Fees struct {
	FreeTierTxPerMonth       uint64
	MDRBasisPoints           uint32
	OwnerWallet              string
	TransferFreeTierSpendWei string
	TransferFreeTierWindow   string
	TransferFeeCollector     string
	// TransferFeeBps is the protocol-enforced fee, in basis points of the
	// transfer amount, charged on an NHB transfer once the sender has
	// exceeded TransferFreeTierSpendWei -- replaces the old sender-self-
	// declared GasPrice*GasLimit charge, which had no protocol-enforced
	// floor (a sender's own wallet could set GasPrice=0 and pay nothing
	// even above the free tier). A percentage rather than a flat fee so it
	// scales with value moved -- competitive against the exchange-swap-fee
	// round trips NHB is meant to substitute for, without being negligible
	// at volume or disproportionate on small transfers. See
	// docs/issue30.md item 7b.
	TransferFeeBps uint32
	// TransferFeeBpsZNHB is TransferFeeBps' counterpart for ZNHB transfers
	// -- a separate, independently configurable rate rather than a reuse
	// of TransferFeeBps, since ZNHB is a lower-priced asset with its own
	// use case and was never deliberately meant to share NHB's rate.
	TransferFeeBpsZNHB uint32
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

// Subscriptions captures the runtime configuration for native/subscriptions'
// recurring-billing engine. Treasury is a bech32 address string (not the
// runtime [20]byte form native/subscriptions.Config uses directly) so it
// round-trips through TOML the same way lending.Config.DeveloperFeeCollector
// does -- decoded once at node construction (cmd/nhb/main.go,
// cmd/consensusd/main.go), never re-parsed per block.
type Subscriptions struct {
	// ManagementFeeBps is NHBCoin's own platform fee for running the
	// subscriptions engine, in basis points of each charge -- charged
	// alongside (never instead of) the ordinary transfer fee, since a
	// subscription charge is not a TxTypeTransfer/TxTypeTransferZNHB and
	// never goes through that fee path at all.
	ManagementFeeBps uint32 `toml:"ManagementFeeBps"`
	// ManagementFeeCapBps is a hard ceiling ManagementFeeBps may never
	// exceed.
	ManagementFeeCapBps uint32 `toml:"ManagementFeeCapBps"`
	// Treasury receives every charge's management-fee share. Required
	// once ManagementFeeBps > 0.
	Treasury string `toml:"Treasury"`
	// MaxRetries is how many consecutive failed charge attempts a
	// subscription tolerates before being suspended.
	MaxRetries uint32 `toml:"MaxRetries"`
	// RetryIntervalSeconds spaces out consecutive retry attempts after a
	// failed charge.
	RetryIntervalSeconds uint64 `toml:"RetryIntervalSeconds"`
}

// EnsureDefaults fills unset fields with native/subscriptions' baseline
// defaults, mirroring lending.Config.EnsureDefaults' role in Load.
func (s *Subscriptions) EnsureDefaults() {
	// Only default ManagementFeeBps to a nonzero value when a Treasury is
	// already configured to receive it -- cmd/nhb and cmd/consensusd both
	// panic on startup if ManagementFeeBps>0 with no Treasury set (see
	// commit 075febe, which patched the one checked-in config.toml but not
	// this defaulting logic). A config with neither field set should come
	// up with subscriptions fees disabled, not crash-loop.
	if s.ManagementFeeBps == 0 && s.Treasury != "" {
		s.ManagementFeeBps = subscriptions.DefaultManagementFeeBps
	}
	if s.ManagementFeeCapBps == 0 {
		s.ManagementFeeCapBps = subscriptions.DefaultManagementFeeCapBps
	}
	if s.MaxRetries == 0 {
		s.MaxRetries = subscriptions.DefaultMaxRetries
	}
	if s.RetryIntervalSeconds == 0 {
		s.RetryIntervalSeconds = subscriptions.DefaultRetryIntervalSeconds
	}
}

type Pauses struct {
	Lending       bool
	Swap          bool
	Escrow        bool
	Trade         bool
	Loyalty       bool
	POTSO         bool
	TransferNHB   bool
	TransferZNHB  bool
	Staking       bool
	Subscriptions bool
	// SwapRedeem pauses only new TxTypeRedeemNHB burns (swap-out); it never
	// blocks TxTypeAttestRedemption, so in-flight payouts that already
	// burned their NHB can still be attested paid/failed during a pause.
	SwapRedeem bool
	// Market pauses the peer-to-peer ZNHB-for-NHB marketplace
	// (native/market): new listings, fills, and cancellations all gate on
	// this single flag -- unlike SwapRedeem there is no in-flight-payout
	// concern to carve out, since every market state transition settles
	// atomically within its own transaction.
	Market bool
}

// Quota defines rate limits for module interactions on a per-address basis.
type Quota struct {
	MaxRequestsPerMin uint32
	MaxNHBPerEpoch    uint64 // in gwei or base units
	EpochSeconds      uint32 // e.g., 3600
}

// Quotas groups quotas for each module. Every module below has at least one
// real applyQuota(...) call site in core/state_transition.go's
// handleNativeTransaction: Swap gates TxTypeBuyZNHB/BuybackAsk/RedeemNHB;
// Loyalty gates TxTypeCreateLoyaltyBusiness/CreateLoyaltyProgram (the two
// operations that mint a new ID -- SetPaymaster/AddMerchant/RemoveMerchant/
// UpdateProgram/PauseProgram/ResumeProgram are deliberately left
// unconfigured/unenforced, see their TxType doc comments). Subscriptions
// and Market had real enforcement call sites but no config field at all
// until this was wired up -- added below so they can actually be
// configured, matching the other enforced modules.
type Quotas struct {
	Lending       Quota
	Swap          Quota
	Escrow        Quota
	Trade         Quota
	Loyalty       Quota
	POTSO         Quota
	Subscriptions Quota
	Market        Quota
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
