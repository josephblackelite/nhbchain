package payoutd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration to support YAML unmarshalling.
type Duration struct {
	time.Duration
}

// UnmarshalYAML parses human readable duration strings.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be string")
	}
	raw := value.Value
	if raw == "" {
		d.Duration = 0
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

// Config captures the runtime configuration for payoutd.
type Config struct {
	ListenAddress string            `yaml:"listen"`
	PoliciesPath  string            `yaml:"policies"`
	Authority     string            `yaml:"authority"`
	PauseOnStart  bool              `yaml:"pause"`
	PollInterval  Duration          `yaml:"poll_interval"`
	Consensus     ConsensusConfig   `yaml:"consensus"`
	Wallet        WalletConfig      `yaml:"wallet"`
	Inventory     map[string]string `yaml:"inventory"`
	Admin         AdminConfig       `yaml:"admin"`
}

// AdminConfig captures security settings for the admin API.
type AdminConfig struct {
	BearerToken     string         `yaml:"bearer_token"`
	BearerTokenFile string         `yaml:"bearer_token_file"`
	MTLS            MTLSConfig     `yaml:"mtls"`
	TLS             AdminTLSConfig `yaml:"tls"`
}

// MTLSConfig controls mutual TLS verification.
type MTLSConfig struct {
	Enabled      bool   `yaml:"enabled"`
	ClientCAPath string `yaml:"client_ca"`
}

// AdminTLSConfig configures TLS certificates for the admin API.
type AdminTLSConfig struct {
	Disable  bool   `yaml:"disable"`
	CertPath string `yaml:"cert"`
	KeyPath  string `yaml:"key"`
}

// ConsensusConfig configures the consensus client used to emit receipts.
type ConsensusConfig struct {
	Endpoint      string `yaml:"endpoint"`
	ChainID       string `yaml:"chain_id"`
	SignerKey     string `yaml:"signer_key"`
	SignerKeyFile string `yaml:"signer_key_file"`
	SignerKeyEnv  string `yaml:"signer_key_env"`
	FeeAmount     string `yaml:"fee_amount"`
	FeeDenom      string `yaml:"fee_denom"`
	FeePayer      string `yaml:"fee_payer"`
	Memo          string `yaml:"memo"`
}

// WalletConfig captures parameters for the treasury hot wallet.
type WalletConfig struct {
	Confirmations int      `yaml:"confirmations"`
	PollInterval  Duration `yaml:"poll_interval"`
}

// LoadConfig reads configuration from the supplied path.
func LoadConfig(path string) (Config, error) {
	cfg := Config{}
	file, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	dec := yaml.NewDecoder(file)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}
	applyDefaults(&cfg)
	if err := cfg.Consensus.normalise(); err != nil {
		return cfg, fmt.Errorf("consensus signer: %w", err)
	}
	if err := cfg.Admin.normalise(); err != nil {
		return cfg, fmt.Errorf("admin security: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = ":7082"
	}
	if cfg.PoliciesPath == "" {
		cfg.PoliciesPath = "services/payoutd/policies.yaml"
	}
	if cfg.PollInterval.Duration == 0 {
		cfg.PollInterval.Duration = 5 * time.Second
	}
	if cfg.Wallet.PollInterval.Duration == 0 {
		cfg.Wallet.PollInterval.Duration = 3 * time.Second
	}
	if cfg.Wallet.Confirmations <= 0 {
		cfg.Wallet.Confirmations = 3
	}
	if cfg.Inventory == nil {
		cfg.Inventory = map[string]string{}
	}
}

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.Authority) == "" {
		return fmt.Errorf("authority must be configured")
	}
	if strings.TrimSpace(cfg.Consensus.Endpoint) == "" {
		return fmt.Errorf("consensus endpoint must be configured")
	}
	if strings.TrimSpace(cfg.Consensus.ChainID) == "" {
		return fmt.Errorf("consensus chain_id must be configured")
	}
	if strings.TrimSpace(cfg.Consensus.SignerKey) == "" {
		return fmt.Errorf("signer key must be configured")
	}
	if cfg.Admin.BearerToken == "" && !cfg.Admin.MTLS.Enabled {
		return fmt.Errorf("configure either bearer_token or mTLS for admin authentication")
	}
	return nil
}

func (c *ConsensusConfig) normalise() error {
	if c == nil {
		return fmt.Errorf("consensus configuration missing")
	}
	c.SignerKey = strings.TrimSpace(c.SignerKey)
	c.SignerKeyEnv = strings.TrimSpace(c.SignerKeyEnv)
	c.SignerKeyFile = strings.TrimSpace(c.SignerKeyFile)
	if c.SignerKey != "" {
		return nil
	}
	switch {
	case c.SignerKeyEnv != "":
		value := strings.TrimSpace(os.Getenv(c.SignerKeyEnv))
		if value == "" {
			return fmt.Errorf("signer_key_env %s is empty", c.SignerKeyEnv)
		}
		c.SignerKey = value
	case c.SignerKeyFile != "":
		contents, err := os.ReadFile(c.SignerKeyFile)
		if err != nil {
			return fmt.Errorf("read signer_key_file: %w", err)
		}
		c.SignerKey = strings.TrimSpace(string(contents))
	default:
		return fmt.Errorf("signer_key is required")
	}
	return nil
}

func (a *AdminConfig) normalise() error {
	if a == nil {
		return fmt.Errorf("admin configuration missing")
	}
	token := strings.TrimSpace(a.BearerToken)
	if path := strings.TrimSpace(a.BearerTokenFile); path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read bearer_token_file: %w", err)
		}
		token = strings.TrimSpace(string(contents))
	}
	a.BearerToken = token
	a.MTLS.ClientCAPath = strings.TrimSpace(a.MTLS.ClientCAPath)
	a.TLS.CertPath = strings.TrimSpace(a.TLS.CertPath)
	a.TLS.KeyPath = strings.TrimSpace(a.TLS.KeyPath)
	if a.TLS.CertPath == "" && a.TLS.KeyPath == "" {
		a.TLS.Disable = true
	}
	if !a.TLS.Disable {
		if a.TLS.CertPath == "" {
			return fmt.Errorf("tls.cert must be configured when TLS is enabled")
		}
		if a.TLS.KeyPath == "" {
			return fmt.Errorf("tls.key must be configured when TLS is enabled")
		}
	}
	if a.MTLS.Enabled && a.TLS.Disable {
		return fmt.Errorf("mTLS requires TLS to be enabled")
	}
	return nil
}
