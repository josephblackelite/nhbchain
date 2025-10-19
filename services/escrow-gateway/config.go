package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
