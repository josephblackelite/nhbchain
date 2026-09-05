package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"
)

// APIKeyConfig describes a single API key + secret pair accepted by the gateway.
type APIKeyConfig struct {
	Key      string          `json:"key"`
	Secret   string          `json:"secret"`
	Merchant *MerchantConfig `json:"merchant,omitempty"`
}

// MerchantConfig captures merchant-specific defaults and constraints.
type MerchantConfig struct {
	Identity string              `json:"identity,omitempty"`
	Realm    MerchantRealmConfig `json:"realm"`
}

// MerchantRealmConfig controls realm defaults and enforcement for a merchant.
type MerchantRealmConfig struct {
	Default              string `json:"default,omitempty"`
	Scope                string `json:"scope,omitempty"`
	Type                 string `json:"type,omitempty"`
	EnforceIdentityMatch bool   `json:"enforceIdentityMatch,omitempty"`
}

// Config captures runtime configuration for the escrow gateway service.
type Config struct {
	ListenAddress        string
	NodeURL              string
	NodeAuthToken        string
	DatabasePath         string
	AllowedTimestampSkew time.Duration
	NonceTTL             time.Duration
	NonceCapacity        int
	APIKeys              []APIKeyConfig
	MerchantConfigs      map[string]MerchantConfig
	WebhookQueueCapacity int
	WebhookHistorySize   int
	WebhookQueueTTL      time.Duration

	// RelayerKMSEnv names another environment variable holding the
	// gateway's own raw hex-encoded secp256k1 private key -- an extra
	// level of indirection mirroring services/payments-gateway's
	// AttestorKMSEnv, so the actual secret value can live in a
	// deployment-chosen variable name rather than a hardcoded one. This
	// key signs every transaction the gateway submits to the chain
	// (TxTypeDelegated{Create,Release,Refund,Dispute}Escrow) -- it pays
	// gas and owns the transaction's nonce, but never becomes an escrow's
	// payer/payee/mediator; authorization for the underlying action comes
	// entirely from the participant signature embedded in each
	// transaction's payload (see native/escrow/engine.go's
	// escrowActionEnvelope/escrowCreateEnvelope). Required -- without it
	// the gateway can still serve read endpoints (escrow_get, and
	// anything not gated behind RelayerReady) but every mutating escrow
	// endpoint fails closed.
	RelayerKMSEnv string

	// RelayerMinBalanceWei/RelayerBalanceCheckInterval configure the
	// periodic low-balance monitor (main.go) added after this service ran
	// in production for a while with literally nothing watching whether
	// its relayer's gas balance was running low -- see
	// docs/escrow/nhbchain-escrow-gateway.md's Production Deployment
	// section. Every interval, the gateway logs a WARN (structured JSON,
	// scrapeable by the same Prometheus/Alertmanager pipeline
	// docs/runbooks/alerts.md already describes for consensus-node
	// metrics) if the relayer's balance is at or below this threshold.
	// This is deliberately a log-line alert, not a Slack/PagerDuty
	// integration -- no such integration exists anywhere else in this
	// codebase (see docs/runbooks/alerts.md: alerting is Alertmanager/
	// PagerDuty, external to this repo), so a new one here would be
	// inconsistent with how every other service in this project surfaces
	// operational problems.
	RelayerMinBalanceWei        *big.Int
	RelayerBalanceCheckInterval time.Duration
}

// LoadConfigFromEnv builds a configuration using environment variables.
func LoadConfigFromEnv() (Config, error) {
	cfg := Config{
		ListenAddress:        getenvDefault("ESCROW_GATEWAY_LISTEN", ":8081"),
		NodeURL:              os.Getenv("ESCROW_GATEWAY_NODE_URL"),
		NodeAuthToken:        os.Getenv("ESCROW_GATEWAY_NODE_TOKEN"),
		DatabasePath:         getenvDefault("ESCROW_GATEWAY_DB_PATH", "escrow-gateway.db"),
		AllowedTimestampSkew: 2 * time.Minute,
		NonceCapacity:        1024,
		WebhookQueueCapacity: defaultTaskCapacity,
		WebhookHistorySize:   defaultHistoryCapacity,
		WebhookQueueTTL:      defaultQueueTTL,
	}

	if skew := strings.TrimSpace(os.Getenv("ESCROW_GATEWAY_TIMESTAMP_SKEW")); skew != "" {
		if dur, err := time.ParseDuration(skew); err == nil {
			cfg.AllowedTimestampSkew = dur
		} else {
			return Config{}, err
		}
	}

	cfg.NonceTTL = 2 * cfg.AllowedTimestampSkew
	if raw := strings.TrimSpace(os.Getenv("ESCROW_GATEWAY_NONCE_TTL")); raw != "" {
		dur, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse ESCROW_GATEWAY_NONCE_TTL: %w", err)
		}
		if dur <= 0 {
			return Config{}, errors.New("ESCROW_GATEWAY_NONCE_TTL must be positive")
		}
		cfg.NonceTTL = dur
	}
	if cfg.NonceTTL < cfg.AllowedTimestampSkew {
		cfg.NonceTTL = cfg.AllowedTimestampSkew
	}

	if raw := strings.TrimSpace(os.Getenv("ESCROW_GATEWAY_NONCE_CAP")); raw != "" {
		val, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse ESCROW_GATEWAY_NONCE_CAP: %w", err)
		}
		if val <= 0 {
			return Config{}, errors.New("ESCROW_GATEWAY_NONCE_CAP must be positive")
		}
		cfg.NonceCapacity = val
	}

	if cfg.NodeURL == "" {
		return Config{}, errors.New("ESCROW_GATEWAY_NODE_URL is required")
	}

	cfg.RelayerKMSEnv = strings.TrimSpace(os.Getenv("ESCROW_GATEWAY_RELAYER_KMS_ENV"))
	if cfg.RelayerKMSEnv == "" {
		return Config{}, errors.New("ESCROW_GATEWAY_RELAYER_KMS_ENV is required -- names the env var holding the gateway's relayer private key")
	}

	// Default threshold: 1 NHB. Native transactions are fee-free (gas is
	// never actually charged, see core/state_transition.go), so this isn't
	// "enough gas for N more transactions" the way it would be on a
	// fee-charging chain -- it's a low bar meant only to catch the relayer
	// balance going to (or toward) zero, e.g. an operator error draining it,
	// not a transaction-volume-driven depletion. Override via
	// ESCROW_GATEWAY_RELAYER_MIN_BALANCE_WEI for a deployment-specific
	// threshold.
	cfg.RelayerMinBalanceWei = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	if raw := strings.TrimSpace(os.Getenv("ESCROW_GATEWAY_RELAYER_MIN_BALANCE_WEI")); raw != "" {
		val, ok := new(big.Int).SetString(raw, 10)
		if !ok || val.Sign() < 0 {
			return Config{}, errors.New("ESCROW_GATEWAY_RELAYER_MIN_BALANCE_WEI must be a non-negative base-10 integer")
		}
		cfg.RelayerMinBalanceWei = val
	}

	cfg.RelayerBalanceCheckInterval = 10 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("ESCROW_GATEWAY_RELAYER_BALANCE_CHECK_INTERVAL")); raw != "" {
		dur, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse ESCROW_GATEWAY_RELAYER_BALANCE_CHECK_INTERVAL: %w", err)
		}
		if dur <= 0 {
			return Config{}, errors.New("ESCROW_GATEWAY_RELAYER_BALANCE_CHECK_INTERVAL must be positive")
		}
		cfg.RelayerBalanceCheckInterval = dur
	}

	if raw := strings.TrimSpace(os.Getenv("ESCROW_GATEWAY_QUEUE_CAP")); raw != "" {
		val, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse ESCROW_GATEWAY_QUEUE_CAP: %w", err)
		}
		if val <= 0 {
			return Config{}, errors.New("ESCROW_GATEWAY_QUEUE_CAP must be positive")
		}
		cfg.WebhookQueueCapacity = val
	}

	if raw := strings.TrimSpace(os.Getenv("ESCROW_GATEWAY_QUEUE_HISTORY")); raw != "" {
		val, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse ESCROW_GATEWAY_QUEUE_HISTORY: %w", err)
		}
		if val <= 0 {
			return Config{}, errors.New("ESCROW_GATEWAY_QUEUE_HISTORY must be positive")
		}
		cfg.WebhookHistorySize = val
	}

	if raw := strings.TrimSpace(os.Getenv("ESCROW_GATEWAY_QUEUE_TTL")); raw != "" {
		dur, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse ESCROW_GATEWAY_QUEUE_TTL: %w", err)
		}
		if dur <= 0 {
			return Config{}, errors.New("ESCROW_GATEWAY_QUEUE_TTL must be positive")
		}
		cfg.WebhookQueueTTL = dur
	}

	// Parse API keys from JSON array: [{"key":"...","secret":"..."}, ...]
	apiJSON := strings.TrimSpace(os.Getenv("ESCROW_GATEWAY_API_KEYS"))
	if apiJSON == "" {
		return Config{}, errors.New("ESCROW_GATEWAY_API_KEYS is required")
	}
	var entries []APIKeyConfig
	if err := json.Unmarshal([]byte(apiJSON), &entries); err != nil {
		return Config{}, err
	}
	cfg.MerchantConfigs = make(map[string]MerchantConfig)
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		secret := strings.TrimSpace(entry.Secret)
		if key == "" || secret == "" {
			return Config{}, errors.New("api key entries must include key and secret")
		}
		sanitized := APIKeyConfig{Key: key, Secret: secret}
		if entry.Merchant != nil {
			merchant, err := sanitizeMerchantConfig(key, *entry.Merchant)
			if err != nil {
				return Config{}, err
			}
			cfg.MerchantConfigs[key] = merchant
			sanitized.Merchant = &merchant
		}
		cfg.APIKeys = append(cfg.APIKeys, sanitized)
	}

	if len(cfg.APIKeys) == 0 {
		return Config{}, errors.New("no API keys configured")
	}

	return cfg, nil
}

func getenvDefault(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}

// parseUnixTimestamp parses a unix timestamp in seconds from the supplied string.
func parseUnixTimestamp(v string) (time.Time, error) {
	secs, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(secs, 0).UTC(), nil
}

func sanitizeMerchantConfig(apiKey string, input MerchantConfig) (MerchantConfig, error) {
	merchant := MerchantConfig{
		Identity: strings.TrimSpace(input.Identity),
		Realm: MerchantRealmConfig{
			Default:              strings.TrimSpace(input.Realm.Default),
			Scope:                strings.ToLower(strings.TrimSpace(input.Realm.Scope)),
			Type:                 strings.ToLower(strings.TrimSpace(input.Realm.Type)),
			EnforceIdentityMatch: input.Realm.EnforceIdentityMatch,
		},
	}
	if merchant.Identity == "" {
		merchant.Identity = strings.TrimSpace(apiKey)
	}
	if l := len(merchant.Realm.Default); l > 0 && l > 64 {
		return MerchantConfig{}, fmt.Errorf("merchant realm default exceeds 64 characters")
	}
	if l := len(merchant.Identity); l > 0 && l > 128 {
		return MerchantConfig{}, fmt.Errorf("merchant identity exceeds 128 characters")
	}
	if merchant.Realm.Scope != "" && merchant.Realm.Scope != "platform" && merchant.Realm.Scope != "marketplace" {
		return MerchantConfig{}, fmt.Errorf("unsupported merchant realm scope: %s", merchant.Realm.Scope)
	}
	if merchant.Realm.Type != "" && merchant.Realm.Type != "public" && merchant.Realm.Type != "private" {
		return MerchantConfig{}, fmt.Errorf("unsupported merchant realm type: %s", merchant.Realm.Type)
	}
	if merchant.Realm.EnforceIdentityMatch && merchant.Realm.Type != "private" {
		merchant.Realm.Type = "private"
	}
	if merchant.Realm.Default == "" && merchant.Realm.EnforceIdentityMatch {
		merchant.Realm.Default = merchant.Identity
	}
	return merchant, nil
}
