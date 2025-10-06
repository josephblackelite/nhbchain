package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config captures the runtime settings for the lending service daemon.
type Config struct {
	ListenAddress string     `yaml:"listen"`
	TLS           TLSConfig  `yaml:"tls"`
	Auth          AuthConfig `yaml:"auth"`
}

// TLSConfig describes the TLS material for the gRPC server.
type TLSConfig struct {
	CertPath      string `yaml:"cert"`
	KeyPath       string `yaml:"key"`
	ClientCAPath  string `yaml:"client_ca"`
	AllowInsecure bool   `yaml:"allow_insecure"`
}

// AuthConfig lists the authenticators accepted by the service.
type AuthConfig struct {
	APITokens []string       `yaml:"api_tokens"`
	MTLS      MTLSAuthConfig `yaml:"mtls"`
}

// MTLSAuthConfig enumerates the allowed client certificate identities.
type MTLSAuthConfig struct {
	AllowedCommonNames []string `yaml:"allowed_common_names"`
}

// Load reads the YAML configuration from disk and validates the result.
func Load(path string) (Config, error) {
	cfg := Config{
		ListenAddress: ":50053",
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

	cfg.normalize()
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg *Config) normalize() {
	if cfg == nil {
		return
	}
	cfg.ListenAddress = strings.TrimSpace(cfg.ListenAddress)
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = ":50053"
	}
	cfg.TLS.normalize()
	cfg.Auth.normalize()
}

func (cfg *Config) validate() error {
	if cfg == nil {
		return fmt.Errorf("configuration is missing")
	}
	if err := cfg.TLS.validate(); err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	if err := cfg.Auth.validate(cfg.TLS); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	return nil
}

func (cfg *TLSConfig) normalize() {
	if cfg == nil {
		return
	}
	cfg.CertPath = strings.TrimSpace(cfg.CertPath)
	cfg.KeyPath = strings.TrimSpace(cfg.KeyPath)
	cfg.ClientCAPath = strings.TrimSpace(cfg.ClientCAPath)
}

func (cfg TLSConfig) validate() error {
	hasCert := cfg.CertPath != ""
	hasKey := cfg.KeyPath != ""
	if hasCert != hasKey {
		return fmt.Errorf("cert and key must either both be provided or both be empty")
	}
	if !cfg.AllowInsecure && !hasCert {
		return fmt.Errorf("cert and key are required unless allow_insecure=true")
	}
	if cfg.ClientCAPath != "" && !hasCert {
		return fmt.Errorf("client_ca requires a server certificate and key")
	}
	return nil
}

// MTLSEnabled reports whether mutual TLS verification is configured.
func (cfg TLSConfig) MTLSEnabled() bool {
	return strings.TrimSpace(cfg.ClientCAPath) != ""
}

func (cfg *AuthConfig) normalize() {
	if cfg == nil {
		return
	}
	tokens := make([]string, 0, len(cfg.APITokens))
	for _, token := range cfg.APITokens {
		if trimmed := strings.TrimSpace(token); trimmed != "" {
			tokens = append(tokens, trimmed)
		}
	}
	cfg.APITokens = tokens

	names := make([]string, 0, len(cfg.MTLS.AllowedCommonNames))
	for _, name := range cfg.MTLS.AllowedCommonNames {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	cfg.MTLS.AllowedCommonNames = names
}

func (cfg AuthConfig) validate(tls TLSConfig) error {
	hasTokens := len(cfg.APITokens) > 0
	hasMTLS := len(cfg.MTLS.AllowedCommonNames) > 0
	if !hasTokens && !hasMTLS {
		return fmt.Errorf("at least one api token or mTLS common name must be configured")
	}
	if hasMTLS && strings.TrimSpace(tls.ClientCAPath) == "" {
		return fmt.Errorf("mtls.allowed_common_names requires tls.client_ca to be configured")
	}
	return nil
}
