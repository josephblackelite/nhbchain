package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config captures the runtime settings for the governance service.
type Config struct {
	ListenAddress     string       `yaml:"listen"`
	ConsensusEndpoint string       `yaml:"consensus"`
	ChainID           string       `yaml:"chain_id"`
	SignerKey         string       `yaml:"signer_key"`
	NonceStart        uint64       `yaml:"nonce_start"`
	Fee               FeeConfig    `yaml:"fee"`
	TLS               TLSConfig    `yaml:"tls"`
	Auth              AuthConfig   `yaml:"auth"`
	ConsensusClient   ClientConfig `yaml:"consensus_client"`
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

// ClientConfig describes the security configuration for the consensus client.
type ClientConfig struct {
	AllowInsecure bool               `yaml:"allow_insecure"`
	TLS           ClientTLSConfig    `yaml:"tls"`
	SharedSecret  SharedSecretConfig `yaml:"shared_secret"`
}

// ClientTLSConfig captures optional TLS material for the consensus client.
type ClientTLSConfig struct {
	CertPath string `yaml:"cert"`
	KeyPath  string `yaml:"key"`
	CAPath   string `yaml:"ca"`
}

// SharedSecretConfig provides metadata for shared-secret authentication.
type SharedSecretConfig struct {
	Header string `yaml:"header"`
	Token  string `yaml:"token"`
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
	if err := cfg.ConsensusClient.Validate(); err != nil {
		return cfg, fmt.Errorf("consensus client security: %w", err)
	}
	return cfg, nil
}

// Validate ensures the consensus client security configuration is well formed.
func (cfg *ClientConfig) Validate() error {
	if cfg == nil {
		return fmt.Errorf("configuration is missing")
	}
	cfg.SharedSecret.Header = strings.TrimSpace(cfg.SharedSecret.Header)
	cfg.SharedSecret.Token = strings.TrimSpace(cfg.SharedSecret.Token)

	hasTLSCert := strings.TrimSpace(cfg.TLS.CertPath) != ""
	hasTLSKey := strings.TrimSpace(cfg.TLS.KeyPath) != ""
	if hasTLSCert != hasTLSKey {
		return fmt.Errorf("tls cert and key must both be provided when enabling client mTLS")
	}

	hasTLS := strings.TrimSpace(cfg.TLS.CAPath) != "" || (hasTLSCert && hasTLSKey)
	hasSharedSecret := cfg.SharedSecret.Token != ""

	if !cfg.AllowInsecure && !hasTLS && !hasSharedSecret {
		return fmt.Errorf("requires tls material or shared-secret authentication unless allow_insecure=true")
	}

	return nil
}
