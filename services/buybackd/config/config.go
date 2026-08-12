// Package config loads buybackd's runtime configuration from environment
// variables, following the same env-var-driven pattern services/lending's
// main package uses (see services/lending/config.go) rather than a YAML
// file, since buybackd has a small, flat configuration surface.
//
// Secrets follow two different conventions on purpose. The chain RPC bearer
// token is read directly from an environment variable, matching
// LEND_NODE_RPC_TOKEN's precedent elsewhere in this repo. Keystore
// passphrases are read through an extra layer of indirection -- this
// package only ever collects the NAME of an environment variable holding
// each passphrase, never the passphrase itself -- because
// services/swapd/localsigner.Config already enforces that discipline at the
// type level (see its PassphraseEnv field) and buybackd reuses that package
// directly for signing.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	envRPCURL                 = "BUYBACKD_RPC_URL"
	envRPCBearerToken         = "BUYBACKD_RPC_BEARER_TOKEN"
	envRPCTimeoutSeconds      = "BUYBACKD_RPC_TIMEOUT_SECONDS"
	envPair                   = "BUYBACKD_PAIR"
	envThreshold              = "BUYBACKD_THRESHOLD"
	envPollIntervalSeconds    = "BUYBACKD_POLL_INTERVAL_SECONDS"
	envMaxQuoteAgeSeconds     = "BUYBACKD_MAX_QUOTE_AGE_SECONDS"
	envOraclePriority         = "BUYBACKD_ORACLE_PRIORITY"
	envManualRate             = "BUYBACKD_MANUAL_RATE"
	envNOWPaymentsAPIKey      = "BUYBACKD_NOWPAYMENTS_API_KEY"
	envKeystorePaths          = "BUYBACKD_KEYSTORE_PATHS"
	envKeystorePassphraseEnvs = "BUYBACKD_KEYSTORE_PASSPHRASE_ENVS"

	defaultRPCURL              = "https://127.0.0.1:8081"
	defaultRPCTimeoutSeconds   = 15
	defaultPair                = "ZNHB/USD"
	defaultThreshold           = 2
	defaultPollIntervalSeconds = 300
	defaultMaxQuoteAgeSeconds  = 120
)

// SignerConfig names one locally held keystore: its file path and the NAME
// of the environment variable holding its decryption passphrase. This
// mirrors services/swapd/localsigner.Config's field shape exactly, since
// it is converted directly into one at startup.
type SignerConfig struct {
	KeystorePath  string
	PassphraseEnv string
}

// Config captures buybackd's full runtime configuration.
type Config struct {
	RPCBaseURL        string
	RPCBearerToken    string
	RPCTimeout        time.Duration
	Pair              string
	Threshold         int
	PollInterval      time.Duration
	MaxQuoteAge       time.Duration
	OraclePriority    []string
	ManualRate        string
	NOWPaymentsAPIKey string
	Signers           []SignerConfig
}

// LoadFromEnv constructs a Config from environment variables and defaults.
// It does not validate the result -- call Validate separately so callers
// that want to inspect a partially-loaded config (e.g. to log it) can do so
// before deciding whether to fail.
func LoadFromEnv() Config {
	cfg := Config{
		RPCBaseURL:        stringFromEnv(envRPCURL, defaultRPCURL),
		RPCBearerToken:    strings.TrimSpace(os.Getenv(envRPCBearerToken)),
		RPCTimeout:        time.Duration(intFromEnv(envRPCTimeoutSeconds, defaultRPCTimeoutSeconds)) * time.Second,
		Pair:              stringFromEnv(envPair, defaultPair),
		Threshold:         intFromEnv(envThreshold, defaultThreshold),
		PollInterval:      time.Duration(intFromEnv(envPollIntervalSeconds, defaultPollIntervalSeconds)) * time.Second,
		MaxQuoteAge:       time.Duration(intFromEnv(envMaxQuoteAgeSeconds, defaultMaxQuoteAgeSeconds)) * time.Second,
		OraclePriority:    splitAndTrim(os.Getenv(envOraclePriority)),
		ManualRate:        strings.TrimSpace(os.Getenv(envManualRate)),
		NOWPaymentsAPIKey: strings.TrimSpace(os.Getenv(envNOWPaymentsAPIKey)),
		Signers:           loadSigners(),
	}
	if len(cfg.OraclePriority) == 0 {
		cfg.OraclePriority = []string{"manual", "nowpayments", "coingecko"}
	}
	return cfg
}

func loadSigners() []SignerConfig {
	paths := splitAndTrim(os.Getenv(envKeystorePaths))
	passphraseEnvs := splitAndTrim(os.Getenv(envKeystorePassphraseEnvs))
	signers := make([]SignerConfig, 0, len(paths))
	for i, path := range paths {
		signer := SignerConfig{KeystorePath: path}
		if i < len(passphraseEnvs) {
			signer.PassphraseEnv = passphraseEnvs[i]
		}
		signers = append(signers, signer)
	}
	return signers
}

// Sanitized returns a copy of cfg with secrets masked, safe to log.
func (cfg Config) Sanitized() Config {
	clone := cfg
	if clone.RPCBearerToken != "" {
		clone.RPCBearerToken = "***"
	}
	return clone
}

// Validate checks that cfg is internally consistent and refuses to run with
// an under-provisioned signer set -- an operator running with fewer local
// keys than the threshold requires would otherwise start successfully and
// then silently fail every submission attempt forever, which is a worse
// failure mode than refusing to start.
func (cfg Config) Validate() error {
	if strings.TrimSpace(cfg.RPCBaseURL) == "" {
		return fmt.Errorf("config: %s is required", envRPCURL)
	}
	if strings.TrimSpace(cfg.RPCBearerToken) == "" {
		return fmt.Errorf("config: %s is required", envRPCBearerToken)
	}
	if cfg.Threshold <= 0 {
		return fmt.Errorf("config: %s must be positive", envThreshold)
	}
	if len(cfg.Signers) == 0 {
		return fmt.Errorf("config: at least one keystore required (%s)", envKeystorePaths)
	}
	if len(cfg.Signers) < cfg.Threshold {
		return fmt.Errorf("config: %d keystore(s) configured, need at least %d to reach the configured threshold", len(cfg.Signers), cfg.Threshold)
	}
	for i, signer := range cfg.Signers {
		if strings.TrimSpace(signer.KeystorePath) == "" {
			return fmt.Errorf("config: keystore %d has an empty path", i)
		}
		if strings.TrimSpace(signer.PassphraseEnv) == "" {
			return fmt.Errorf("config: keystore %d (%s) has no matching passphrase environment variable name in %s", i, signer.KeystorePath, envKeystorePassphraseEnvs)
		}
	}
	if _, _, err := splitPair(cfg.Pair); err != nil {
		return err
	}
	if cfg.PollInterval <= 0 {
		return fmt.Errorf("config: %s must be positive", envPollIntervalSeconds)
	}
	hasManual := false
	for _, name := range cfg.OraclePriority {
		if strings.EqualFold(name, "manual") {
			hasManual = true
			break
		}
	}
	if hasManual && strings.TrimSpace(cfg.ManualRate) == "" {
		return fmt.Errorf("config: %s is required when \"manual\" is in %s", envManualRate, envOraclePriority)
	}
	return nil
}

func splitPair(pair string) (base, quote string, err error) {
	trimmed := strings.TrimSpace(pair)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("config: invalid %s %q (want BASE/QUOTE)", envPair, pair)
	}
	base = strings.TrimSpace(parts[0])
	quote = strings.TrimSpace(parts[1])
	if base == "" || quote == "" {
		return "", "", fmt.Errorf("config: invalid %s %q (want BASE/QUOTE)", envPair, pair)
	}
	return base, quote, nil
}

func stringFromEnv(key, fallback string) string {
	trimmed := strings.TrimSpace(os.Getenv(key))
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func intFromEnv(key string, fallback int) int {
	trimmed := strings.TrimSpace(os.Getenv(key))
	if trimmed == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitAndTrim(value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}
