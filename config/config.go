package config

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nhbchain/consensus"
	"nhbchain/core/genesis"
	"nhbchain/crypto"
	"nhbchain/native/fees"
	"nhbchain/native/governance"
	"nhbchain/native/lending"
	"nhbchain/native/potso"
	swap "nhbchain/native/swap"

	"github.com/BurntSushi/toml"
)

var defaultAllowedGovernanceParams = []string{
	"fees.baseFee",
	governance.ParamKeyMinimumValidatorStake,
	governance.ParamKeyStakingAprBps,
	governance.ParamKeyStakingPayoutPeriodDays,
	governance.ParamKeyStakingUnbondingDays,
	governance.ParamKeyStakingMinStakeWei,
	governance.ParamKeyStakingMaxEmissionPerYearWei,
	governance.ParamKeyStakingRewardAsset,
	governance.ParamKeyStakingCompoundDefault,
	governance.ParamKeyLoyaltyDynamicTargetBps,
	governance.ParamKeyLoyaltyDynamicMinBps,
	governance.ParamKeyLoyaltyDynamicMaxBps,
	governance.ParamKeyLoyaltyDynamicSmoothingStepBps,
	governance.ParamKeyLoyaltyDynamicCoverageMax,
	governance.ParamKeyLoyaltyDynamicCoverageLookbackDays,
	governance.ParamKeyLoyaltyDynamicDailyCapPctOf7dFees,
	governance.ParamKeyLoyaltyDynamicDailyCapUSD,
	governance.ParamKeyLoyaltyDynamicYearlyCapPctOfInitialSupply,
	governance.ParamKeyLoyaltyDynamicPricePair,
	governance.ParamKeyLoyaltyDynamicPriceTwapWindowSeconds,
	governance.ParamKeyLoyaltyDynamicPriceMaxAgeSeconds,
	governance.ParamKeyLoyaltyDynamicPriceMaxDeviationBps,
	governance.ParamKeyLoyaltyDynamicPriceGuardEnabled,
	"network.seeds",
	"potso.abuse.MaxUserShareBps",
	"potso.abuse.MinStakeToEarnWei",
	"potso.abuse.QuadraticTxDampenAfter",
	"potso.abuse.QuadraticTxDampenPower",
	"potso.rewards.EmissionPerEpochWei",
	"potso.weights.AlphaStakeBps",
}

type Config struct {
	ListenAddress         string          `toml:"ListenAddress"`
	RPCAddress            string          `toml:"RPCAddress"`
	RPCTrustedProxies     []string        `toml:"RPCTrustedProxies"`
	RPCTrustProxyHeaders  bool            `toml:"RPCTrustProxyHeaders"`
	RPCReadHeaderTimeout  int             `toml:"RPCReadHeaderTimeout"`
	RPCReadTimeout        int             `toml:"RPCReadTimeout"`
	RPCWriteTimeout       int             `toml:"RPCWriteTimeout"`
	RPCIdleTimeout        int             `toml:"RPCIdleTimeout"`
	RPCAllowInsecure      bool            `toml:"RPCAllowInsecure"`
	RPCTLSCertFile        string          `toml:"RPCTLSCertFile"`
	RPCTLSKeyFile         string          `toml:"RPCTLSKeyFile"`
	RPCTLSClientCAFile    string          `toml:"RPCTLSClientCAFile"`
	DataDir               string          `toml:"DataDir"`
	GenesisFile           string          `toml:"GenesisFile"`
	AllowAutogenesis      bool            `toml:"AllowAutogenesis"`
	ValidatorKeystorePath string          `toml:"ValidatorKeystorePath"`
	ValidatorKMSURI       string          `toml:"ValidatorKMSURI"`
	ValidatorKMSEnv       string          `toml:"ValidatorKMSEnv"`
	NetworkName           string          `toml:"NetworkName"`
	Bootnodes             []string        `toml:"Bootnodes"`
	PersistentPeers       []string        `toml:"PersistentPeers"`
	BootstrapPeers        []string        `toml:"BootstrapPeers,omitempty"`
	MaxPeers              int             `toml:"MaxPeers"`
	MaxInbound            int             `toml:"MaxInbound"`
	MaxOutbound           int             `toml:"MaxOutbound"`
	MinPeers              int             `toml:"MinPeers"`
	OutboundPeers         int             `toml:"OutboundPeers"`
	PeerBanSeconds        int             `toml:"PeerBanSeconds"`
	ReadTimeout           int             `toml:"ReadTimeout"`
	WriteTimeout          int             `toml:"WriteTimeout"`
	MaxMsgBytes           int             `toml:"MaxMsgBytes"`
	MaxMsgsPerSecond      float64         `toml:"MaxMsgsPerSecond"`
	ClientVersion         string          `toml:"ClientVersion"`
	P2P                   P2PSection      `toml:"p2p"`
	Potso                 PotsoConfig     `toml:"potso"`
	Governance            GovConfig       `toml:"governance"`
	Swap                  swap.Config     `toml:"swap"`
	Lending               lending.Config  `toml:"lending"`
	Mempool               MempoolConfig   `toml:"mempool"`
	Global                Global          `toml:"global"`
	Consensus             Consensus       `toml:"consensus"`
	NetworkSecurity       NetworkSecurity `toml:"network_security"`
}

// NetworkSecurity captures TLS and shared-secret settings for the internal gRPC
// bridge between consensusd and p2pd.
type NetworkSecurity struct {
	ServerTLSCertFile         string   `toml:"ServerTLSCertFile"`
	ServerTLSKeyFile          string   `toml:"ServerTLSKeyFile"`
	ServerCAFile              string   `toml:"ServerCAFile"`
	ClientCAFile              string   `toml:"ClientCAFile"`
	ClientTLSCertFile         string   `toml:"ClientTLSCertFile"`
	ClientTLSKeyFile          string   `toml:"ClientTLSKeyFile"`
	AllowInsecure             bool     `toml:"AllowInsecure"`
	AllowUnauthenticatedReads bool     `toml:"AllowUnauthenticatedReads"`
	SharedSecret              string   `toml:"SharedSecret"`
	SharedSecretFile          string   `toml:"SharedSecretFile"`
	SharedSecretEnv           string   `toml:"SharedSecretEnv"`
	AuthorizationHeader       string   `toml:"AuthorizationHeader"`
	AllowedClientCommonNames  []string `toml:"AllowedClientCommonNames"`
	ServerName                string   `toml:"ServerName"`
	StreamQueueSize           int      `toml:"StreamQueueSize"`
	RelayDropLogRatio         float64  `toml:"RelayDropLogRatio"`
}

// AuthorizationHeaderName returns the metadata header that carries the
// shared-secret token. Defaults to "authorization" when unspecified.
func (ns NetworkSecurity) AuthorizationHeaderName() string {
	header := strings.TrimSpace(ns.AuthorizationHeader)
	if header == "" {
		return "authorization"
	}
	return strings.ToLower(header)
}

// ResolveSharedSecret locates the shared-secret token following the precedence
// order of environment variable, external file, and inline configuration.
// Relative file paths are resolved against baseDir when provided.
func (ns NetworkSecurity) ResolveSharedSecret(baseDir string, lookup func(string) (string, bool)) (string, error) {
	if key := strings.TrimSpace(ns.SharedSecretEnv); key != "" && lookup != nil {
		if value, ok := lookup(key); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed, nil
			}
		}
	}

	if path := strings.TrimSpace(ns.SharedSecretFile); path != "" {
		if baseDir != "" && !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if secret := strings.TrimSpace(string(data)); secret != "" {
			return secret, nil
		}
	}

	return strings.TrimSpace(ns.SharedSecret), nil
}

func defaultGlobalConfig() Global {
	return Global{
		Governance: Governance{
			QuorumBPS:        6000,
			PassThresholdBPS: 5000,
			VotingPeriodSecs: 604800,
		},
		Slashing: Slashing{
			MinWindowSecs: 60,
			MaxWindowSecs: 600,
		},
		Mempool: Mempool{MaxBytes: 16 << 20, POSReservationBPS: consensus.DefaultPOSReservationBPS},
		Blocks:  Blocks{MaxTxs: 5000},
		Staking: Staking{
			AprBps:                1250,
			PayoutPeriodDays:      30,
			UnbondingDays:         7,
			MinStakeWei:           "1",
			MaxEmissionPerYearWei: "5000000000000000000",
			RewardAsset:           "ZNHB",
			CompoundDefault:       false,
		},
		Pauses: Pauses{},
		Paymaster: Paymaster{
			MerchantDailyCapWei: "0",
			GlobalDailyCapWei:   "0",
			AutoTopUp: PaymasterAutoTopUp{
				Token:          "ZNHB",
				MinBalanceWei:  "0",
				TopUpAmountWei: "0",
				DailyCapWei:    "0",
				Governance: PaymasterAutoTopUpGovernance{
					MinterRole:   "MINTER_ZNHB",
					ApproverRole: "ROLE_PAYMASTER_ADMIN",
				},
			},
		},
		Fees: Fees{
			FreeTierTxPerMonth: DefaultFreeTierTxPerMonth,
			MDRBasisPoints:     DefaultMDRBasisPoints,
			OwnerWallet:        "nhb1tctz3yvhrwztnp6ds3s48qp4jgfujcvhgxxpka",
			Assets: []FeeAsset{
				{Asset: fees.AssetNHB, MDRBasisPoints: DefaultMDRBasisPoints, OwnerWallet: "nhb1tctz3yvhrwztnp6ds3s48qp4jgfujcvhgxxpka"},
				{Asset: fees.AssetZNHB, MDRBasisPoints: DefaultMDRBasisPoints, OwnerWallet: "znhb19l75s7jkyzxp4z7lj3ddgn9r89y3kps54wpv0w"},
			},
		},
		Loyalty: Loyalty{
			Dynamic: LoyaltyDynamic{
				TargetBPS:                   defaultLoyaltyTargetBPS,
				MinBPS:                      defaultLoyaltyMinBPS,
				MaxBPS:                      defaultLoyaltyMaxBPS,
				SmoothingStepBPS:            defaultLoyaltySmoothingStepBPS,
				CoverageMax:                 defaultLoyaltyCoverageMax,
				CoverageLookbackDays:        defaultLoyaltyCoverageLookbackDays,
				DailyCapPctOf7dFees:         defaultLoyaltyDailyCapPctOf7dFees,
				DailyCapUSD:                 defaultLoyaltyDailyCapUSD,
				YearlyCapPctOfInitialSupply: defaultLoyaltyYearlyCapPctOfInitialSupply,
				PriceGuard: LoyaltyPriceGuard{
					Enabled:            true,
					PricePair:          defaultLoyaltyPricePair,
					TwapWindowSeconds:  defaultLoyaltyPriceGuardTwapWindowSeconds,
					MaxDeviationBPS:    defaultLoyaltyPriceGuardMaxDeviation,
					PriceMaxAgeSeconds: defaultLoyaltyPriceGuardMaxAgeSeconds,
				},
				EnableProRate: true,
			},
		},
	}
}

func defaultConsensusConfig() Consensus {
	return Consensus{
		ProposalTimeout:  2 * time.Second,
		PrevoteTimeout:   2 * time.Second,
		PrecommitTimeout: 2 * time.Second,
		CommitTimeout:    4 * time.Second,
	}
}

const (
	defaultBlockTimestampToleranceSeconds = 5
	defaultStreamQueueSize                = 128
	defaultRelayDropLogRatio              = 0.1
	// DefaultMempoolMaxTransactions bounds pending transactions when no explicit limit is provided.
	DefaultMempoolMaxTransactions             = 4000
	defaultLoyaltyTargetBPS                   = 50
	defaultLoyaltyMinBPS                      = 25
	defaultLoyaltyMaxBPS                      = 100
	defaultLoyaltySmoothingStepBPS            = 5
	defaultLoyaltyCoverageMax                 = 0.50
	defaultLoyaltyCoverageLookbackDays        = 7
	defaultLoyaltyDailyCapPctOf7dFees         = 0.60
	defaultLoyaltyDailyCapUSD                 = 5_000.0
	defaultLoyaltyYearlyCapPctOfInitialSupply = 10.0
	defaultLoyaltyPricePair                   = "ZNHB/USD"
	defaultLoyaltyPriceGuardTwapWindowSeconds = uint32(7_200)
	defaultLoyaltyPriceGuardMaxDeviation      = 300
	defaultLoyaltyPriceGuardMaxAgeSeconds     = uint32(600)
)

// P2PSection captures nested configuration for the peer-to-peer subsystem.
type P2PSection struct {
	NetworkID           uint64   `toml:"NetworkId"`
	MaxPeers            int      `toml:"MaxPeers"`
	MaxInbound          int      `toml:"MaxInbound"`
	MaxOutbound         int      `toml:"MaxOutbound"`
	MinPeers            int      `toml:"MinPeers"`
	OutboundPeers       int      `toml:"OutboundPeers"`
	Bootnodes           []string `toml:"Bootnodes"`
	PersistentPeers     []string `toml:"PersistentPeers"`
	Seeds               []string `toml:"Seeds"`
	BanScore            int      `toml:"BanScore"`
	GreyScore           int      `toml:"GreyScore"`
	RateMsgsPerSec      float64  `toml:"RateMsgsPerSec"`
	Burst               float64  `toml:"Burst"`
	HandshakeTimeoutMs  int      `toml:"HandshakeTimeoutMs"`
	PingIntervalSeconds int      `toml:"PingIntervalSeconds"`
	PingTimeoutSeconds  int      `toml:"PingTimeoutSeconds"`
	BanDurationSeconds  int      `toml:"BanDurationSeconds"`
	DialBackoffSeconds  int      `toml:"DialBackoffSeconds"`
	PEX                 *bool    `toml:"PEX"`
}

// PotsoConfig groups POTSO-specific configuration segments.
type PotsoConfig struct {
	Rewards PotsoRewardsConfig `toml:"rewards"`
	Weights PotsoWeightsConfig `toml:"weights"`
	Abuse   PotsoAbuseConfig   `toml:"abuse"`
}

// PotsoRewardsConfig mirrors the TOML structure for POTSO reward distribution parameters.
type PotsoRewardsConfig struct {
	EpochLengthBlocks  uint64 `toml:"EpochLengthBlocks"`
	AlphaStakeBps      uint64 `toml:"AlphaStakeBps"`
	MinPayoutWei       string `toml:"MinPayoutWei"`
	EmissionPerEpoch   string `toml:"EmissionPerEpoch"`
	TreasuryAddress    string `toml:"TreasuryAddress"`
	MaxWinnersPerEpoch uint64 `toml:"MaxWinnersPerEpoch"`
	CarryRemainder     bool   `toml:"CarryRemainder"`
	PayoutMode         string `toml:"PayoutMode"`
}

// PotsoWeightsConfig mirrors the `[potso.weights]` TOML section.
type PotsoWeightsConfig struct {
	AlphaStakeBps         uint64 `toml:"AlphaStakeBps"`
	TxWeightBps           uint64 `toml:"TxWeightBps"`
	EscrowWeightBps       uint64 `toml:"EscrowWeightBps"`
	UptimeWeightBps       uint64 `toml:"UptimeWeightBps"`
	MaxEngagementPerEpoch uint64 `toml:"MaxEngagementPerEpoch"`
	MinStakeToWinWei      string `toml:"MinStakeToWinWei"`
	MinEngagementToWin    uint64 `toml:"MinEngagementToWin"`
	DecayHalfLifeEpochs   uint64 `toml:"DecayHalfLifeEpochs"`
	TopKWinners           uint64 `toml:"TopKWinners"`
	TieBreak              string `toml:"TieBreak"`
}

// PotsoAbuseConfig captures anti-abuse controls for POTSO emissions and weights.
type PotsoAbuseConfig struct {
	MinStakeToEarnWei      string `toml:"MinStakeToEarnWei"`
	QuadraticTxDampenAfter uint64 `toml:"QuadraticTxDampenAfter"`
	QuadraticTxDampenPower uint64 `toml:"QuadraticTxDampenPower"`
	MaxUserShareBps        uint64 `toml:"MaxUserShareBps"`
}

// MempoolConfig allows operators to tune the size of the transaction pool.
type MempoolConfig struct {
	MaxTransactions int  `toml:"MaxTransactions"`
	AllowUnlimited  bool `toml:"AllowUnlimited"`
}

// GovConfig captures the governance policy knobs controlling proposal flow
// without embedding business logic in the state machine.
type GovConfig struct {
	MinDepositWei                  string   `toml:"MinDepositWei"`
	VotingPeriodSeconds            uint64   `toml:"VotingPeriodSeconds"`
	TimelockSeconds                uint64   `toml:"TimelockSeconds"`
	QuorumBps                      uint64   `toml:"QuorumBps"`
	PassThresholdBps               uint64   `toml:"PassThresholdBps"`
	AllowedParams                  []string `toml:"AllowedParams"`
	AllowedRoles                   []string `toml:"AllowedRoles"`
	TreasuryAllowList              []string `toml:"TreasuryAllowList"`
	BlockTimestampToleranceSeconds uint64   `toml:"BlockTimestampToleranceSeconds"`
}

type LoadOption func(*loadOptions)

type loadOptions struct {
	keystorePassphrase       string
	keystorePassphraseSource func() (string, error)
}

// WithKeystorePassphrase provides an explicit passphrase to use when the loader
// needs to create or update the validator keystore.
func WithKeystorePassphrase(passphrase string) LoadOption {
	return func(opts *loadOptions) {
		opts.keystorePassphrase = passphrase
	}
}

// WithKeystorePassphraseSource registers a lazy resolver that is invoked only
// when a passphrase is required (for example, on first-run when no keystore
// exists). The resolved value is cached so subsequent calls return the same
// passphrase.
func WithKeystorePassphraseSource(source func() (string, error)) LoadOption {
	return func(opts *loadOptions) {
		opts.keystorePassphraseSource = source
	}
}

// Load loads the configuration from the given path.
func Load(path string, opts ...LoadOption) (*Config, error) {
	options := loadOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	passphrase := options.keystorePassphrase
	havePassphrase := passphrase != ""
	resolvePassphrase := func() (string, error) {
		if havePassphrase {
			return passphrase, nil
		}
		if options.keystorePassphraseSource == nil {
			return "", nil
		}
		value, err := options.keystorePassphraseSource()
		if err != nil {
			return "", err
		}
		passphrase = value
		havePassphrase = true
		return passphrase, nil
	}

	cfg := &Config{}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		resolved, err := resolvePassphrase()
		if err != nil {
			return nil, err
		}
		return createDefault(path, resolved)
	}

	meta, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, err
	}

	for _, undecoded := range meta.Undecoded() {
		if len(undecoded) == 1 && undecoded[0] == "ValidatorKey" {
			return nil, fmt.Errorf("config file %s uses deprecated ValidatorKey field; run nhbctl migrate-keystore", path)
		}
	}

	if cfg.ValidatorKMSURI == "" && cfg.ValidatorKMSEnv == "" {
		if err := ensureKeystore(path, cfg, resolvePassphrase); err != nil {
			return nil, err
		}
	}

	cfg.mergeP2PFromTopLevel()
	cfg.Lending.EnsureDefaults()
	cfg.ensureMempoolDefaults()
	cfg.ensureGlobalDefaults(meta)
	cfg.ensureConsensusDefaults(meta)

	if strings.TrimSpace(cfg.NetworkName) == "" {
		cfg.NetworkName = "nhb-local"
	}
	if cfg.Bootnodes == nil {
		cfg.Bootnodes = []string{}
	}
	if cfg.PersistentPeers == nil {
		cfg.PersistentPeers = []string{}
	}
	if cfg.P2P.Seeds == nil {
		cfg.P2P.Seeds = []string{}
	}
	if len(cfg.Bootnodes) == 0 && len(cfg.BootstrapPeers) > 0 {
		cfg.Bootnodes = append([]string{}, cfg.BootstrapPeers...)
	}
	cfg.BootstrapPeers = nil

	if cfg.MaxPeers <= 0 {
		cfg.MaxPeers = 64
	}
	if cfg.MaxInbound <= 0 || cfg.MaxInbound > cfg.MaxPeers {
		cfg.MaxInbound = cfg.MaxPeers
	}
	if cfg.MaxOutbound <= 0 || cfg.MaxOutbound > cfg.MaxPeers {
		cfg.MaxOutbound = cfg.MaxPeers
	}
	if cfg.MinPeers <= 0 || cfg.MinPeers > cfg.MaxPeers {
		cfg.MinPeers = cfg.MaxPeers / 2
		if cfg.MinPeers <= 0 {
			cfg.MinPeers = 1
		}
	}
	if cfg.OutboundPeers <= 0 || cfg.OutboundPeers > cfg.MaxOutbound {
		cfg.OutboundPeers = cfg.MaxOutbound
	}
	if cfg.PeerBanSeconds <= 0 {
		cfg.PeerBanSeconds = int((60 * time.Minute).Seconds())
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = int((90 * time.Second).Seconds())
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = int((5 * time.Second).Seconds())
	}
	if cfg.MaxMsgBytes <= 0 {
		cfg.MaxMsgBytes = 1 << 20
	}
	if cfg.MaxMsgsPerSecond <= 0 {
		cfg.MaxMsgsPerSecond = 32
	}
	if strings.TrimSpace(cfg.ClientVersion) == "" {
		cfg.ClientVersion = "nhbchain/node"
	}
	if cfg.NetworkSecurity.StreamQueueSize <= 0 {
		cfg.NetworkSecurity.StreamQueueSize = defaultStreamQueueSize
	}
	if cfg.NetworkSecurity.RelayDropLogRatio <= 0 {
		cfg.NetworkSecurity.RelayDropLogRatio = defaultRelayDropLogRatio
	} else if cfg.NetworkSecurity.RelayDropLogRatio > 1 {
		cfg.NetworkSecurity.RelayDropLogRatio = 1
	}

	if cfg.Governance.BlockTimestampToleranceSeconds == 0 {
		cfg.Governance.BlockTimestampToleranceSeconds = defaultBlockTimestampToleranceSeconds
	}

	if strings.TrimSpace(cfg.Potso.Rewards.MinPayoutWei) == "" {
		cfg.Potso.Rewards.MinPayoutWei = "0"
	}
	if strings.TrimSpace(cfg.Potso.Rewards.EmissionPerEpoch) == "" {
		cfg.Potso.Rewards.EmissionPerEpoch = "0"
	}

	weightDefaults := potso.DefaultWeightParams()
	if cfg.Potso.Weights.AlphaStakeBps == 0 {
		cfg.Potso.Weights.AlphaStakeBps = weightDefaults.AlphaStakeBps
	}
	if cfg.Potso.Weights.TxWeightBps == 0 {
		cfg.Potso.Weights.TxWeightBps = weightDefaults.TxWeightBps
	}
	if cfg.Potso.Weights.EscrowWeightBps == 0 {
		cfg.Potso.Weights.EscrowWeightBps = weightDefaults.EscrowWeightBps
	}
	if cfg.Potso.Weights.UptimeWeightBps == 0 {
		cfg.Potso.Weights.UptimeWeightBps = weightDefaults.UptimeWeightBps
	}
	if cfg.Potso.Weights.MaxEngagementPerEpoch == 0 {
		cfg.Potso.Weights.MaxEngagementPerEpoch = weightDefaults.MaxEngagementPerEpoch
	}
	if strings.TrimSpace(cfg.Potso.Weights.MinStakeToWinWei) == "" {
		cfg.Potso.Weights.MinStakeToWinWei = weightDefaults.MinStakeToWinWei.String()
	}
	if strings.TrimSpace(cfg.Potso.Abuse.MinStakeToEarnWei) == "" {
		cfg.Potso.Abuse.MinStakeToEarnWei = weightDefaults.MinStakeToEarnWei.String()
	}
	if cfg.Potso.Weights.DecayHalfLifeEpochs == 0 {
		cfg.Potso.Weights.DecayHalfLifeEpochs = weightDefaults.DecayHalfLifeEpochs
	}
	if cfg.Potso.Weights.TopKWinners == 0 {
		cfg.Potso.Weights.TopKWinners = weightDefaults.TopKWinners
	}
	if strings.TrimSpace(cfg.Potso.Weights.TieBreak) == "" {
		cfg.Potso.Weights.TieBreak = string(weightDefaults.TieBreak)
	}
	if cfg.Potso.Abuse.QuadraticTxDampenPower == 0 {
		cfg.Potso.Abuse.QuadraticTxDampenPower = weightDefaults.QuadraticTxDampenPower
	}

	if strings.TrimSpace(cfg.Governance.MinDepositWei) == "" {
		cfg.Governance.MinDepositWei = "1000e18"
	}
	if cfg.Governance.VotingPeriodSeconds == 0 {
		cfg.Governance.VotingPeriodSeconds = 604800
	}
	if cfg.Governance.TimelockSeconds == 0 {
		cfg.Governance.TimelockSeconds = 172800
	}
	if cfg.Governance.QuorumBps == 0 {
		cfg.Governance.QuorumBps = 2000
	}
	if cfg.Governance.PassThresholdBps == 0 {
		cfg.Governance.PassThresholdBps = 5000
	}
	if len(cfg.Governance.AllowedParams) == 0 {
		cfg.Governance.AllowedParams = append([]string(nil), defaultAllowedGovernanceParams...)
	}

	cfg.Swap = cfg.Swap.Normalise()
	cfg.syncTopLevelToP2P()

	return cfg, nil
}

func (cfg *Config) ensureMempoolDefaults() {
	if cfg.Mempool.AllowUnlimited {
		if cfg.Mempool.MaxTransactions < 0 {
			cfg.Mempool.MaxTransactions = 0
		}
		return
	}
	if cfg.Mempool.MaxTransactions <= 0 {
		cfg.Mempool.MaxTransactions = DefaultMempoolMaxTransactions
	}
}

func (cfg *Config) ensureGlobalDefaults(meta toml.MetaData) {
	defaults := defaultGlobalConfig()

	if !meta.IsDefined("global", "governance", "QuorumBPS") {
		cfg.Global.Governance.QuorumBPS = defaults.Governance.QuorumBPS
	}
	if !meta.IsDefined("global", "governance", "PassThresholdBPS") {
		cfg.Global.Governance.PassThresholdBPS = defaults.Governance.PassThresholdBPS
	}
	if !meta.IsDefined("global", "governance", "VotingPeriodSecs") {
		cfg.Global.Governance.VotingPeriodSecs = defaults.Governance.VotingPeriodSecs
	}

	if !meta.IsDefined("global", "slashing", "MinWindowSecs") {
		cfg.Global.Slashing.MinWindowSecs = defaults.Slashing.MinWindowSecs
	}
	if !meta.IsDefined("global", "slashing", "MaxWindowSecs") {
		cfg.Global.Slashing.MaxWindowSecs = defaults.Slashing.MaxWindowSecs
	}
	if cfg.Global.Slashing.MaxWindowSecs < cfg.Global.Slashing.MinWindowSecs {
		cfg.Global.Slashing.MaxWindowSecs = cfg.Global.Slashing.MinWindowSecs
	}

	if !meta.IsDefined("global", "mempool", "MaxBytes") {
		cfg.Global.Mempool.MaxBytes = defaults.Mempool.MaxBytes
	}
	if !meta.IsDefined("global", "mempool", "POSReservationBPS") {
		cfg.Global.Mempool.POSReservationBPS = defaults.Mempool.POSReservationBPS
	}
	if !meta.IsDefined("global", "blocks", "MaxTxs") {
		cfg.Global.Blocks.MaxTxs = defaults.Blocks.MaxTxs
	}

	if !meta.IsDefined("global", "staking", "AprBps") {
		cfg.Global.Staking.AprBps = defaults.Staking.AprBps
	}
	if !meta.IsDefined("global", "staking", "PayoutPeriodDays") {
		cfg.Global.Staking.PayoutPeriodDays = defaults.Staking.PayoutPeriodDays
	}
	if !meta.IsDefined("global", "staking", "UnbondingDays") {
		cfg.Global.Staking.UnbondingDays = defaults.Staking.UnbondingDays
	}
	if strings.TrimSpace(cfg.Global.Staking.MinStakeWei) == "" {
		cfg.Global.Staking.MinStakeWei = defaults.Staking.MinStakeWei
	}
	if strings.TrimSpace(cfg.Global.Staking.MaxEmissionPerYearWei) == "" {
		cfg.Global.Staking.MaxEmissionPerYearWei = defaults.Staking.MaxEmissionPerYearWei
	}
	if strings.TrimSpace(cfg.Global.Staking.RewardAsset) == "" {
		cfg.Global.Staking.RewardAsset = defaults.Staking.RewardAsset
	}
	if !meta.IsDefined("global", "staking", "CompoundDefault") {
		cfg.Global.Staking.CompoundDefault = defaults.Staking.CompoundDefault
	}

	if strings.TrimSpace(cfg.Global.Paymaster.MerchantDailyCapWei) == "" {
		cfg.Global.Paymaster.MerchantDailyCapWei = defaults.Paymaster.MerchantDailyCapWei
	}
	if strings.TrimSpace(cfg.Global.Paymaster.GlobalDailyCapWei) == "" {
		cfg.Global.Paymaster.GlobalDailyCapWei = defaults.Paymaster.GlobalDailyCapWei
	}
	if !meta.IsDefined("global", "paymaster", "DeviceDailyTxCap") {
		cfg.Global.Paymaster.DeviceDailyTxCap = defaults.Paymaster.DeviceDailyTxCap
	}
	if !meta.IsDefined("global", "paymaster", "AutoTopUp", "Token") {
		cfg.Global.Paymaster.AutoTopUp.Token = defaults.Paymaster.AutoTopUp.Token
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.MinBalanceWei) == "" {
		cfg.Global.Paymaster.AutoTopUp.MinBalanceWei = defaults.Paymaster.AutoTopUp.MinBalanceWei
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.TopUpAmountWei) == "" {
		cfg.Global.Paymaster.AutoTopUp.TopUpAmountWei = defaults.Paymaster.AutoTopUp.TopUpAmountWei
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.DailyCapWei) == "" {
		cfg.Global.Paymaster.AutoTopUp.DailyCapWei = defaults.Paymaster.AutoTopUp.DailyCapWei
	}
	if !meta.IsDefined("global", "paymaster", "AutoTopUp", "CooldownSeconds") {
		cfg.Global.Paymaster.AutoTopUp.CooldownSeconds = defaults.Paymaster.AutoTopUp.CooldownSeconds
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.Governance.MinterRole) == "" {
		cfg.Global.Paymaster.AutoTopUp.Governance.MinterRole = defaults.Paymaster.AutoTopUp.Governance.MinterRole
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.Governance.ApproverRole) == "" {
		cfg.Global.Paymaster.AutoTopUp.Governance.ApproverRole = defaults.Paymaster.AutoTopUp.Governance.ApproverRole
	}
	if !meta.IsDefined("global", "fees", "FreeTierTxPerMonth") {
		cfg.Global.Fees.FreeTierTxPerMonth = defaults.Fees.FreeTierTxPerMonth
	}
	if !meta.IsDefined("global", "fees", "MDRBasisPoints") {
		cfg.Global.Fees.MDRBasisPoints = defaults.Fees.MDRBasisPoints
	}
	if !meta.IsDefined("global", "fees", "OwnerWallet") {
		cfg.Global.Fees.OwnerWallet = defaults.Fees.OwnerWallet
	}
	if len(cfg.Global.Fees.Assets) == 0 {
		cfg.Global.Fees.Assets = append([]FeeAsset{}, defaults.Fees.Assets...)
	}

	if !meta.IsDefined("global", "loyalty", "Dynamic", "TargetBPS") {
		cfg.Global.Loyalty.Dynamic.TargetBPS = defaults.Loyalty.Dynamic.TargetBPS
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "MinBPS") {
		cfg.Global.Loyalty.Dynamic.MinBPS = defaults.Loyalty.Dynamic.MinBPS
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "MaxBPS") {
		cfg.Global.Loyalty.Dynamic.MaxBPS = defaults.Loyalty.Dynamic.MaxBPS
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "SmoothingStepBPS") {
		cfg.Global.Loyalty.Dynamic.SmoothingStepBPS = defaults.Loyalty.Dynamic.SmoothingStepBPS
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "CoverageMax") {
		cfg.Global.Loyalty.Dynamic.CoverageMax = defaults.Loyalty.Dynamic.CoverageMax
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "CoverageLookbackDays") {
		cfg.Global.Loyalty.Dynamic.CoverageLookbackDays = defaults.Loyalty.Dynamic.CoverageLookbackDays
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "DailyCapPctOf7dFees") {
		cfg.Global.Loyalty.Dynamic.DailyCapPctOf7dFees = defaults.Loyalty.Dynamic.DailyCapPctOf7dFees
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "DailyCapUSD") {
		cfg.Global.Loyalty.Dynamic.DailyCapUSD = defaults.Loyalty.Dynamic.DailyCapUSD
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "YearlyCapPctOfInitialSupply") {
		cfg.Global.Loyalty.Dynamic.YearlyCapPctOfInitialSupply = defaults.Loyalty.Dynamic.YearlyCapPctOfInitialSupply
	}
	if strings.TrimSpace(cfg.Global.Loyalty.Dynamic.PriceGuard.PricePair) == "" {
		cfg.Global.Loyalty.Dynamic.PriceGuard.PricePair = defaults.Loyalty.Dynamic.PriceGuard.PricePair
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "PriceGuard", "TwapWindowSeconds") {
		cfg.Global.Loyalty.Dynamic.PriceGuard.TwapWindowSeconds = defaults.Loyalty.Dynamic.PriceGuard.TwapWindowSeconds
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "PriceGuard", "PriceMaxAgeSeconds") {
		cfg.Global.Loyalty.Dynamic.PriceGuard.PriceMaxAgeSeconds = defaults.Loyalty.Dynamic.PriceGuard.PriceMaxAgeSeconds
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "PriceGuard", "Enabled") {
		cfg.Global.Loyalty.Dynamic.PriceGuard.Enabled = defaults.Loyalty.Dynamic.PriceGuard.Enabled
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "PriceGuard", "MaxDeviationBPS") {
		cfg.Global.Loyalty.Dynamic.PriceGuard.MaxDeviationBPS = defaults.Loyalty.Dynamic.PriceGuard.MaxDeviationBPS
	}
	if !meta.IsDefined("global", "loyalty", "Dynamic", "EnableProRate") {
		cfg.Global.Loyalty.Dynamic.EnableProRate = defaults.Loyalty.Dynamic.EnableProRate
	}
}

func (cfg *Config) ensureConsensusDefaults(meta toml.MetaData) {
	defaults := defaultConsensusConfig()

	if !meta.IsDefined("consensus", "ProposalTimeout") {
		cfg.Consensus.ProposalTimeout = defaults.ProposalTimeout
	}
	if !meta.IsDefined("consensus", "PrevoteTimeout") {
		cfg.Consensus.PrevoteTimeout = defaults.PrevoteTimeout
	}
	if !meta.IsDefined("consensus", "PrecommitTimeout") {
		cfg.Consensus.PrecommitTimeout = defaults.PrecommitTimeout
	}
	if !meta.IsDefined("consensus", "CommitTimeout") {
		cfg.Consensus.CommitTimeout = defaults.CommitTimeout
	}
}

// PotsoRewardConfig converts the loaded TOML representation into the runtime configuration structure.
func (cfg *Config) PotsoRewardConfig() (potso.RewardConfig, error) {
	rewards := cfg.Potso.Rewards
	result := potso.DefaultRewardConfig()
	result.EpochLengthBlocks = rewards.EpochLengthBlocks
	if cfg.Potso.Weights.AlphaStakeBps > 0 {
		result.AlphaStakeBps = cfg.Potso.Weights.AlphaStakeBps
	} else {
		result.AlphaStakeBps = rewards.AlphaStakeBps
	}
	result.MaxWinnersPerEpoch = rewards.MaxWinnersPerEpoch
	result.CarryRemainder = rewards.CarryRemainder
	result.MaxUserShareBps = cfg.Potso.Abuse.MaxUserShareBps

	result.MinPayoutWei = big.NewInt(0)
	trimmedMin := strings.TrimSpace(rewards.MinPayoutWei)
	if trimmedMin != "" {
		value, ok := new(big.Int).SetString(trimmedMin, 10)
		if !ok {
			return result, fmt.Errorf("invalid MinPayoutWei value: %s", rewards.MinPayoutWei)
		}
		result.MinPayoutWei = value
	}

	result.EmissionPerEpoch = big.NewInt(0)
	trimmedEmission := strings.TrimSpace(rewards.EmissionPerEpoch)
	if trimmedEmission != "" {
		value, ok := new(big.Int).SetString(trimmedEmission, 10)
		if !ok {
			return result, fmt.Errorf("invalid EmissionPerEpoch value: %s", rewards.EmissionPerEpoch)
		}
		result.EmissionPerEpoch = value
	}

	trimmedTreasury := strings.TrimSpace(rewards.TreasuryAddress)
	if trimmedTreasury != "" {
		addr, err := genesis.ParseBech32Account(trimmedTreasury)
		if err != nil {
			return result, fmt.Errorf("invalid TreasuryAddress: %w", err)
		}
		result.TreasuryAddress = addr
	}

	trimmedMode := strings.TrimSpace(rewards.PayoutMode)
	if trimmedMode == "" {
		result.PayoutMode = potso.RewardPayoutModeAuto
	} else {
		result.PayoutMode = potso.RewardPayoutMode(trimmedMode).Normalise()
	}

	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

func (cfg *Config) mergeP2PFromTopLevel() {
	if cfg.P2P.MaxPeers == 0 && cfg.MaxPeers > 0 {
		cfg.P2P.MaxPeers = cfg.MaxPeers
	}
	if cfg.MaxPeers == 0 && cfg.P2P.MaxPeers > 0 {
		cfg.MaxPeers = cfg.P2P.MaxPeers
	}
	if cfg.P2P.MaxInbound == 0 && cfg.MaxInbound > 0 {
		cfg.P2P.MaxInbound = cfg.MaxInbound
	}
	if cfg.MaxInbound == 0 && cfg.P2P.MaxInbound > 0 {
		cfg.MaxInbound = cfg.P2P.MaxInbound
	}
	if cfg.P2P.MaxOutbound == 0 && cfg.MaxOutbound > 0 {
		cfg.P2P.MaxOutbound = cfg.MaxOutbound
	}
	if cfg.MaxOutbound == 0 && cfg.P2P.MaxOutbound > 0 {
		cfg.MaxOutbound = cfg.P2P.MaxOutbound
	}
	if cfg.P2P.MinPeers == 0 && cfg.MinPeers > 0 {
		cfg.P2P.MinPeers = cfg.MinPeers
	}
	if cfg.MinPeers == 0 && cfg.P2P.MinPeers > 0 {
		cfg.MinPeers = cfg.P2P.MinPeers
	}
	if cfg.P2P.OutboundPeers == 0 && cfg.OutboundPeers > 0 {
		cfg.P2P.OutboundPeers = cfg.OutboundPeers
	}
	if cfg.OutboundPeers == 0 && cfg.P2P.OutboundPeers > 0 {
		cfg.OutboundPeers = cfg.P2P.OutboundPeers
	}
	if len(cfg.P2P.Bootnodes) == 0 && len(cfg.Bootnodes) > 0 {
		cfg.P2P.Bootnodes = append([]string{}, cfg.Bootnodes...)
	}
	if len(cfg.Bootnodes) == 0 && len(cfg.P2P.Bootnodes) > 0 {
		cfg.Bootnodes = append([]string{}, cfg.P2P.Bootnodes...)
	}
	if len(cfg.P2P.PersistentPeers) == 0 && len(cfg.PersistentPeers) > 0 {
		cfg.P2P.PersistentPeers = append([]string{}, cfg.PersistentPeers...)
	}
	if len(cfg.PersistentPeers) == 0 && len(cfg.P2P.PersistentPeers) > 0 {
		cfg.PersistentPeers = append([]string{}, cfg.P2P.PersistentPeers...)
	}
	if cfg.P2P.RateMsgsPerSec == 0 && cfg.MaxMsgsPerSecond > 0 {
		cfg.P2P.RateMsgsPerSec = cfg.MaxMsgsPerSecond
	}
	if cfg.MaxMsgsPerSecond == 0 && cfg.P2P.RateMsgsPerSec > 0 {
		cfg.MaxMsgsPerSecond = cfg.P2P.RateMsgsPerSec
	}
	if cfg.P2P.Bootnodes == nil {
		cfg.P2P.Bootnodes = []string{}
	}
	if cfg.P2P.PersistentPeers == nil {
		cfg.P2P.PersistentPeers = []string{}
	}
	if cfg.P2P.HandshakeTimeoutMs == 0 {
		cfg.P2P.HandshakeTimeoutMs = 5000
	}
	if cfg.P2P.Burst == 0 {
		cfg.P2P.Burst = 200
	}
	if cfg.P2P.BanScore == 0 {
		cfg.P2P.BanScore = 100
	}
	if cfg.P2P.GreyScore == 0 {
		cfg.P2P.GreyScore = 50
	}
	if cfg.P2P.BanDurationSeconds == 0 && cfg.PeerBanSeconds > 0 {
		cfg.P2P.BanDurationSeconds = cfg.PeerBanSeconds
	}
	if cfg.PeerBanSeconds == 0 && cfg.P2P.BanDurationSeconds > 0 {
		cfg.PeerBanSeconds = cfg.P2P.BanDurationSeconds
	}
	if cfg.P2P.DialBackoffSeconds == 0 {
		cfg.P2P.DialBackoffSeconds = 30
	}
	if cfg.P2P.PEX == nil {
		enabled := true
		cfg.P2P.PEX = &enabled
	}
}

func (cfg *Config) syncTopLevelToP2P() {
	cfg.P2P.MaxPeers = cfg.MaxPeers
	cfg.P2P.MaxInbound = cfg.MaxInbound
	cfg.P2P.MaxOutbound = cfg.MaxOutbound
	cfg.P2P.MinPeers = cfg.MinPeers
	cfg.P2P.OutboundPeers = cfg.OutboundPeers
	cfg.P2P.Bootnodes = append([]string{}, cfg.Bootnodes...)
	cfg.P2P.PersistentPeers = append([]string{}, cfg.PersistentPeers...)
	cfg.P2P.RateMsgsPerSec = cfg.MaxMsgsPerSecond
	if cfg.P2P.HandshakeTimeoutMs <= 0 {
		cfg.P2P.HandshakeTimeoutMs = 5000
	}
	if cfg.P2P.Burst <= 0 {
		cfg.P2P.Burst = 200
	}
	if cfg.P2P.BanScore <= 0 {
		cfg.P2P.BanScore = 100
	}
	if cfg.P2P.GreyScore <= 0 {
		cfg.P2P.GreyScore = 50
	}
	if cfg.P2P.BanDurationSeconds <= 0 {
		cfg.P2P.BanDurationSeconds = cfg.PeerBanSeconds
	}
	if cfg.P2P.BanDurationSeconds <= 0 {
		cfg.P2P.BanDurationSeconds = int((15 * time.Minute).Seconds())
	}
	if cfg.P2P.DialBackoffSeconds <= 0 {
		cfg.P2P.DialBackoffSeconds = 30
	}
	if cfg.P2P.PEX == nil {
		enabled := true
		cfg.P2P.PEX = &enabled
	}
}

// PotsoWeightConfig converts the weights TOML representation into runtime parameters.
func (cfg *Config) PotsoWeightConfig() (potso.WeightParams, error) {
	weights := cfg.Potso.Weights
	result := potso.DefaultWeightParams()
	result.AlphaStakeBps = weights.AlphaStakeBps
	result.TxWeightBps = weights.TxWeightBps
	result.EscrowWeightBps = weights.EscrowWeightBps
	result.UptimeWeightBps = weights.UptimeWeightBps
	result.MaxEngagementPerEpoch = weights.MaxEngagementPerEpoch
	result.MinEngagementToWin = weights.MinEngagementToWin
	result.DecayHalfLifeEpochs = weights.DecayHalfLifeEpochs
	result.TopKWinners = weights.TopKWinners
	if strings.TrimSpace(weights.TieBreak) != "" {
		result.TieBreak = potso.TieBreakMode(strings.TrimSpace(weights.TieBreak))
	}

	trimmedStake := strings.TrimSpace(weights.MinStakeToWinWei)
	if trimmedStake != "" {
		value, ok := new(big.Int).SetString(trimmedStake, 10)
		if !ok {
			return result, fmt.Errorf("invalid MinStakeToWinWei value: %s", weights.MinStakeToWinWei)
		}
		result.MinStakeToWinWei = value
	} else {
		result.MinStakeToWinWei = big.NewInt(0)
	}

	trimmedEarn := strings.TrimSpace(cfg.Potso.Abuse.MinStakeToEarnWei)
	if trimmedEarn != "" {
		value, ok := new(big.Int).SetString(trimmedEarn, 10)
		if !ok {
			return result, fmt.Errorf("invalid MinStakeToEarnWei value: %s", cfg.Potso.Abuse.MinStakeToEarnWei)
		}
		result.MinStakeToEarnWei = value
	} else {
		result.MinStakeToEarnWei = big.NewInt(0)
	}

	result.QuadraticTxDampenAfter = cfg.Potso.Abuse.QuadraticTxDampenAfter
	result.QuadraticTxDampenPower = cfg.Potso.Abuse.QuadraticTxDampenPower

	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

// SwapSettings exposes the swap configuration with defaults applied.
func (cfg *Config) SwapSettings() swap.Config {
	if cfg == nil {
		return swap.Config{}.Normalise()
	}
	return cfg.Swap.Normalise()
}

// Policy converts the governance TOML representation into a runtime proposal policy.
func (cfg GovConfig) Policy() (governance.ProposalPolicy, error) {
	policy := governance.ProposalPolicy{
		VotingPeriodSeconds:            cfg.VotingPeriodSeconds,
		TimelockSeconds:                cfg.TimelockSeconds,
		AllowedParams:                  append([]string{}, cfg.AllowedParams...),
		QuorumBps:                      cfg.QuorumBps,
		PassThresholdBps:               cfg.PassThresholdBps,
		AllowedRoles:                   append([]string{}, cfg.AllowedRoles...),
		BlockTimestampToleranceSeconds: cfg.BlockTimestampToleranceSeconds,
	}
	amount, err := parseUintAmount(cfg.MinDepositWei)
	if err != nil {
		return policy, fmt.Errorf("invalid MinDepositWei value: %w", err)
	}
	if amount != nil {
		policy.MinDepositWei = amount
	}
	for _, raw := range cfg.TreasuryAllowList {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		addr, err := crypto.DecodeAddress(trimmed)
		if err != nil {
			return policy, fmt.Errorf("invalid TreasuryAllowList entry %q: %w", raw, err)
		}
		bytes := addr.Bytes()
		if len(bytes) != 20 {
			return policy, fmt.Errorf("treasury address must be 20 bytes")
		}
		var entry [20]byte
		copy(entry[:], bytes)
		policy.TreasuryAllowList = append(policy.TreasuryAllowList, entry)
	}
	return policy, nil
}

func parseUintAmount(value string) (*big.Int, error) {
	trimmed := strings.ReplaceAll(strings.TrimSpace(value), "_", "")
	if trimmed == "" {
		return big.NewInt(0), nil
	}
	normalized := trimmed
	var exponent int64
	if idx := strings.IndexAny(normalized, "eE"); idx != -1 {
		expPart := strings.TrimSpace(normalized[idx+1:])
		if expPart == "" {
			return nil, fmt.Errorf("invalid scientific notation")
		}
		expValue, err := strconv.ParseInt(expPart, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid scientific notation")
		}
		exponent = expValue
		normalized = strings.TrimSpace(normalized[:idx])
	}
	normalized = strings.TrimPrefix(normalized, "+")
	if strings.HasPrefix(normalized, "-") {
		return nil, fmt.Errorf("amount must not be negative")
	}
	parts := strings.Split(normalized, ".")
	if len(parts) > 2 {
		return nil, fmt.Errorf("invalid amount format")
	}
	integerPart := parts[0]
	fractionalPart := ""
	if len(parts) == 2 {
		fractionalPart = parts[1]
	}
	digits := integerPart + fractionalPart
	if digits == "" {
		return big.NewInt(0), nil
	}
	if !isDigitString(digits) {
		return nil, fmt.Errorf("invalid amount format")
	}
	fracLen := len(fractionalPart)
	for fracLen > 0 && len(digits) > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		fracLen--
	}
	digits = strings.TrimLeft(digits, "0")
	totalExponent := exponent - int64(fracLen)
	if totalExponent < 0 {
		return nil, fmt.Errorf("amount must be an integer")
	}
	if digits == "" {
		digits = "0"
	}
	if totalExponent > 0 {
		digits += strings.Repeat("0", int(totalExponent))
	}
	amount := new(big.Int)
	if _, ok := amount.SetString(digits, 10); !ok {
		return nil, fmt.Errorf("invalid amount value")
	}
	return amount, nil
}

func isDigitString(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func ensureKeystore(configPath string, cfg *Config, resolvePassphrase func() (string, error)) error {
	keystorePath := cfg.ValidatorKeystorePath
	if keystorePath == "" {
		keystorePath = defaultKeystorePath(configPath)
	}

	if _, err := os.Stat(keystorePath); os.IsNotExist(err) {
		if resolvePassphrase == nil {
			return fmt.Errorf("validator keystore %s does not exist and no passphrase was provided", keystorePath)
		}
		passphrase, perr := resolvePassphrase()
		if perr != nil {
			return perr
		}
		if strings.TrimSpace(passphrase) == "" {
			return fmt.Errorf("validator keystore passphrase is required to create %s", keystorePath)
		}
		key, genErr := crypto.GeneratePrivateKey()
		if genErr != nil {
			return genErr
		}
		if err := crypto.SaveToKeystore(keystorePath, key, passphrase); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	if cfg.ValidatorKeystorePath != keystorePath {
		cfg.ValidatorKeystorePath = keystorePath
		return persist(configPath, cfg)
	}

	return nil
}

// createDefault creates and saves a default configuration file.
func createDefault(path string, passphrase string) (*Config, error) {
	if strings.TrimSpace(passphrase) == "" {
		return nil, fmt.Errorf("validator keystore passphrase is required to initialise %s", path)
	}
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}

	keystorePath := defaultKeystorePath(path)
	if err := crypto.SaveToKeystore(keystorePath, key, passphrase); err != nil {
		return nil, err
	}

	cfg := &Config{
		ListenAddress:        ":6001",
		RPCAddress:           ":8080",
		DataDir:              "./nhb-data",
		GenesisFile:          "",
		AllowAutogenesis:     false,
		NetworkName:          "nhb-local",
		Bootnodes:            []string{},
		PersistentPeers:      []string{},
		MaxPeers:             64,
		MaxInbound:           64,
		MaxOutbound:          64,
		MinPeers:             32,
		OutboundPeers:        16,
		PeerBanSeconds:       int((60 * time.Minute).Seconds()),
		ReadTimeout:          int((90 * time.Second).Seconds()),
		WriteTimeout:         int((5 * time.Second).Seconds()),
		RPCReadHeaderTimeout: int((10 * time.Second).Seconds()),
		RPCReadTimeout:       int((15 * time.Second).Seconds()),
		RPCWriteTimeout:      int((15 * time.Second).Seconds()),
		RPCIdleTimeout:       int((120 * time.Second).Seconds()),
		MaxMsgBytes:          1 << 20,
		MaxMsgsPerSecond:     32,
		ClientVersion:        "nhbchain/node",
	}
	cfg.Mempool.MaxTransactions = DefaultMempoolMaxTransactions
	cfg.NetworkSecurity.StreamQueueSize = defaultStreamQueueSize
	cfg.NetworkSecurity.RelayDropLogRatio = defaultRelayDropLogRatio
	cfg.P2P = P2PSection{
		NetworkID:          187001,
		MaxPeers:           64,
		MaxInbound:         60,
		MaxOutbound:        30,
		MinPeers:           32,
		OutboundPeers:      16,
		Bootnodes:          []string{},
		PersistentPeers:    []string{},
		BanScore:           100,
		GreyScore:          50,
		RateMsgsPerSec:     50,
		Burst:              200,
		HandshakeTimeoutMs: 5000,
		BanDurationSeconds: int((60 * time.Minute).Seconds()),
		DialBackoffSeconds: 30,
	}
	enabledPEX := true
	cfg.P2P.PEX = &enabledPEX
	cfg.Potso.Rewards = PotsoRewardsConfig{
		MinPayoutWei:     "0",
		EmissionPerEpoch: "0",
		CarryRemainder:   true,
		PayoutMode:       string(potso.RewardPayoutModeAuto),
	}
	cfg.Governance = GovConfig{
		MinDepositWei:       "1000e18",
		VotingPeriodSeconds: 604800,
		TimelockSeconds:     172800,
		QuorumBps:           2000,
		PassThresholdBps:    5000,
		AllowedParams:       append([]string(nil), defaultAllowedGovernanceParams...),
	}
	cfg.Global = defaultGlobalConfig()
	cfg.Consensus = defaultConsensusConfig()
	cfg.ValidatorKeystorePath = keystorePath
	cfg.syncTopLevelToP2P()

	if err := persist(path, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func persist(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	return toml.NewEncoder(f).Encode(cfg)
}

func defaultKeystorePath(configPath string) string {
	dir := filepath.Dir(configPath)
	if dir == "." || dir == "" {
		dir = ""
	}
	return filepath.Join(dir, "validator.keystore")
}
