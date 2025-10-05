package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config captures the runtime settings for the governance service.
type Config struct {
	ListenAddress     string     `yaml:"listen"`
	ConsensusEndpoint string     `yaml:"consensus"`
	ChainID           string     `yaml:"chain_id"`
	SignerKey         string     `yaml:"signer_key"`
	NonceStart        uint64     `yaml:"nonce_start"`
	Fee               FeeConfig  `yaml:"fee"`
	TLS               TLSConfig  `yaml:"tls"`
	Auth              AuthConfig `yaml:"auth"`
}

// FeeConfig describes the optional transaction fee metadata attached to
// governance transactions.
type FeeConfig struct {
	Amount string `yaml:"amount"`
	Denom  string `yaml:"denom"`
	Payer  string `yaml:"payer"`
}

// TLSConfig captures the TLS assets required to run the gRPC server.
type TLSConfig struct {
	CertPath     string `yaml:"cert"`
	KeyPath      string `yaml:"key"`
	ClientCAPath string `yaml:"client_ca"`
}

// AuthConfig describes the authentication mechanisms accepted by the service.
type AuthConfig struct {
	APITokens []string       `yaml:"api_tokens"`
	MTLS      MTLSAuthConfig `yaml:"mtls"`
}

// MTLSAuthConfig lists the allowed client certificate identities.
type MTLSAuthConfig struct {
	AllowedCommonNames []string `yaml:"allowed_common_names"`
}

// Load reads the YAML configuration from disk.
func Load(path string) (Config, error) {
	cfg := Config{
		ListenAddress:     ":50061",
		ConsensusEndpoint: "localhost:9090",
		ChainID:           "localnet",
		NonceStart:        1,
	}
	if path == "" {
		return cfg, fmt.Errorf("config path required")
	}
	file, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = ":50061"
	}
	if cfg.ConsensusEndpoint == "" {
		cfg.ConsensusEndpoint = "localhost:9090"
	}
	if cfg.ChainID == "" {
		cfg.ChainID = "localnet"
	}
	if cfg.NonceStart == 0 {
		cfg.NonceStart = 1
	}
	if cfg.SignerKey == "" {
		return cfg, fmt.Errorf("signer_key is required")
	}
	if cfg.TLS.CertPath == "" {
		return cfg, fmt.Errorf("tls.cert is required")
	}
	if cfg.TLS.KeyPath == "" {
		return cfg, fmt.Errorf("tls.key is required")
	}
	return cfg, nil
}
