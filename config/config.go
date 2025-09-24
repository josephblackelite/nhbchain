package config

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nhbchain/core/genesis"
	"nhbchain/crypto"
	"nhbchain/native/potso"

	"github.com/BurntSushi/toml"
)

type Config struct {
	ListenAddress         string      `toml:"ListenAddress"`
	RPCAddress            string      `toml:"RPCAddress"`
	DataDir               string      `toml:"DataDir"`
	GenesisFile           string      `toml:"GenesisFile"`
	ValidatorKeystorePath string      `toml:"ValidatorKeystorePath"`
	ValidatorKMSURI       string      `toml:"ValidatorKMSURI"`
	ValidatorKMSEnv       string      `toml:"ValidatorKMSEnv"`
	NetworkName           string      `toml:"NetworkName"`
	Bootnodes             []string    `toml:"Bootnodes"`
	PersistentPeers       []string    `toml:"PersistentPeers"`
	BootstrapPeers        []string    `toml:"BootstrapPeers,omitempty"`
	MaxPeers              int         `toml:"MaxPeers"`
	MaxInbound            int         `toml:"MaxInbound"`
	MaxOutbound           int         `toml:"MaxOutbound"`
	PeerBanSeconds        int         `toml:"PeerBanSeconds"`
	ReadTimeout           int         `toml:"ReadTimeout"`
	WriteTimeout          int         `toml:"WriteTimeout"`
	MaxMsgBytes           int         `toml:"MaxMsgBytes"`
	MaxMsgsPerSecond      float64     `toml:"MaxMsgsPerSecond"`
	ClientVersion         string      `toml:"ClientVersion"`
	P2P                   P2PSection  `toml:"p2p"`
	Potso                 PotsoConfig `toml:"potso"`
}

// P2PSection captures nested configuration for the peer-to-peer subsystem.
type P2PSection struct {
	NetworkID          uint64   `toml:"NetworkId"`
	MaxPeers           int      `toml:"MaxPeers"`
	MaxInbound         int      `toml:"MaxInbound"`
	MaxOutbound        int      `toml:"MaxOutbound"`
	Bootnodes          []string `toml:"Bootnodes"`
	PersistentPeers    []string `toml:"PersistentPeers"`
	BanScore           int      `toml:"BanScore"`
	GreyScore          int      `toml:"GreyScore"`
	RateMsgsPerSec     float64  `toml:"RateMsgsPerSec"`
	Burst              float64  `toml:"Burst"`
	HandshakeTimeoutMs int      `toml:"HandshakeTimeoutMs"`
}

// PotsoConfig groups POTSO-specific configuration segments.
type PotsoConfig struct {
	Rewards PotsoRewardsConfig `toml:"rewards"`
	Weights PotsoWeightsConfig `toml:"weights"`
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

// Load loads the configuration from the given path.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return createDefault(path)
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
		if err := ensureKeystore(path, cfg); err != nil {
			return nil, err
		}
	}

	cfg.mergeP2PFromTopLevel()

	if strings.TrimSpace(cfg.NetworkName) == "" {
		cfg.NetworkName = "nhb-local"
	}
	if cfg.Bootnodes == nil {
		cfg.Bootnodes = []string{}
	}
	if cfg.PersistentPeers == nil {
		cfg.PersistentPeers = []string{}
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
	if cfg.PeerBanSeconds <= 0 {
		cfg.PeerBanSeconds = int((15 * time.Minute).Seconds())
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
	if cfg.Potso.Weights.DecayHalfLifeEpochs == 0 {
		cfg.Potso.Weights.DecayHalfLifeEpochs = weightDefaults.DecayHalfLifeEpochs
	}
	if cfg.Potso.Weights.TopKWinners == 0 {
		cfg.Potso.Weights.TopKWinners = weightDefaults.TopKWinners
	}
	if strings.TrimSpace(cfg.Potso.Weights.TieBreak) == "" {
		cfg.Potso.Weights.TieBreak = string(weightDefaults.TieBreak)
	}

	cfg.syncTopLevelToP2P()

	return cfg, nil
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
}

func (cfg *Config) syncTopLevelToP2P() {
	cfg.P2P.MaxPeers = cfg.MaxPeers
	cfg.P2P.MaxInbound = cfg.MaxInbound
	cfg.P2P.MaxOutbound = cfg.MaxOutbound
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

	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

func ensureKeystore(configPath string, cfg *Config) error {
	keystorePath := cfg.ValidatorKeystorePath
	if keystorePath == "" {
		keystorePath = defaultKeystorePath(configPath)
	}

	if _, err := os.Stat(keystorePath); os.IsNotExist(err) {
		key, genErr := crypto.GeneratePrivateKey()
		if genErr != nil {
			return genErr
		}
		if err := crypto.SaveToKeystore(keystorePath, key, ""); err != nil {
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
func createDefault(path string) (*Config, error) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}

	keystorePath := defaultKeystorePath(path)
	if err := crypto.SaveToKeystore(keystorePath, key, ""); err != nil {
		return nil, err
	}

	cfg := &Config{
		ListenAddress:    ":6001",
		RPCAddress:       ":8080",
		DataDir:          "./nhb-data",
		GenesisFile:      "",
		NetworkName:      "nhb-local",
		Bootnodes:        []string{},
		PersistentPeers:  []string{},
		MaxPeers:         64,
		MaxInbound:       64,
		MaxOutbound:      64,
		PeerBanSeconds:   int((15 * time.Minute).Seconds()),
		ReadTimeout:      int((90 * time.Second).Seconds()),
		WriteTimeout:     int((5 * time.Second).Seconds()),
		MaxMsgBytes:      1 << 20,
		MaxMsgsPerSecond: 32,
		ClientVersion:    "nhbchain/node",
	}
	cfg.P2P = P2PSection{
		NetworkID:          187001,
		MaxPeers:           64,
		MaxInbound:         60,
		MaxOutbound:        30,
		Bootnodes:          []string{},
		PersistentPeers:    []string{},
		BanScore:           100,
		GreyScore:          50,
		RateMsgsPerSec:     50,
		Burst:              200,
		HandshakeTimeoutMs: 5000,
	}
	cfg.Potso.Rewards = PotsoRewardsConfig{
		MinPayoutWei:     "0",
		EmissionPerEpoch: "0",
		CarryRemainder:   true,
		PayoutMode:       string(potso.RewardPayoutModeAuto),
	}
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
