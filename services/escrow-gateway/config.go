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
	Key    string
	Secret string
}

// Config captures runtime configuration for the escrow gateway service.
type Config struct {
	ListenAddress        string
	NodeURL              string
	NodeAuthToken        string
	DatabasePath         string
	AllowedTimestampSkew time.Duration
	APIKeys              []APIKeyConfig
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
		AllowedTimestampSkew: 5 * time.Minute,
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
	var entries []map[string]string
	if err := json.Unmarshal([]byte(apiJSON), &entries); err != nil {
		return Config{}, err
	}
	for _, entry := range entries {
		key := strings.TrimSpace(entry["key"])
		secret := strings.TrimSpace(entry["secret"])
		if key == "" || secret == "" {
			return Config{}, errors.New("api key entries must include key and secret")
		}
		cfg.APIKeys = append(cfg.APIKeys, APIKeyConfig{Key: key, Secret: secret})
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
