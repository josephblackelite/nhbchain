package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config captures the runtime settings for lendingd.
type Config struct {
	NodeRPCURL         string
	NodeRPCToken       string
	SharedSecretHeader string
	SharedSecretValue  string
	TLSCertFile        string
	TLSKeyFile         string
	TLSClientCAFile    string
	AllowInsecure      bool
	Listen             string
	RateLimitPerMin    int
	MTLSRequired       bool
	AllowedClientCNs   []string
}

const (
	envNodeRPCURL         = "LEND_NODE_RPC_URL"
	envNodeRPCToken       = "LEND_NODE_RPC_TOKEN"
	envSharedSecretHeader = "LEND_SHARED_SECRET_HEADER"
	envSharedSecret       = "LEND_SHARED_SECRET"
	envTLSCertFile        = "LEND_TLS_CERT_FILE"
	envTLSKeyFile         = "LEND_TLS_KEY_FILE"
	envTLSClientCAFile    = "LEND_TLS_CLIENT_CA_FILE"
	envAllowInsecure      = "LEND_ALLOW_INSECURE"
	envListen             = "LEND_LISTEN"
	envRateLimitPerMin    = "LEND_RATE_PER_MIN"
	envMTLSRequired       = "LEND_MTLS_REQUIRED"
	envAllowedCNs         = "LEND_ALLOWED_CNS"

	defaultNodeRPCURL         = "https://127.0.0.1:8081"
	defaultSharedSecretHeader = "X-NHB-Shared-Secret"
	defaultListen             = "0.0.0.0:9444"
	defaultRateLimitPerMin    = 120
)

// LoadConfigFromEnv constructs a Config using environment variables and defaults.
func LoadConfigFromEnv() Config {
	cfg := Config{
		NodeRPCURL:         stringFromEnv(envNodeRPCURL, defaultNodeRPCURL),
		NodeRPCToken:       strings.TrimSpace(os.Getenv(envNodeRPCToken)),
		SharedSecretHeader: stringFromEnv(envSharedSecretHeader, defaultSharedSecretHeader),
		SharedSecretValue:  strings.TrimSpace(os.Getenv(envSharedSecret)),
		TLSCertFile:        strings.TrimSpace(os.Getenv(envTLSCertFile)),
		TLSKeyFile:         strings.TrimSpace(os.Getenv(envTLSKeyFile)),
		TLSClientCAFile:    strings.TrimSpace(os.Getenv(envTLSClientCAFile)),
		AllowInsecure:      boolFromEnv(envAllowInsecure, false),
		Listen:             stringFromEnv(envListen, defaultListen),
		RateLimitPerMin:    intFromEnv(envRateLimitPerMin, defaultRateLimitPerMin),
		MTLSRequired:       boolFromEnv(envMTLSRequired, false),
		AllowedClientCNs:   splitAndTrim(os.Getenv(envAllowedCNs)),
	}
	return cfg
}

// Sanitized returns a copy of the Config with secrets masked for logging.
func (cfg Config) Sanitized() Config {
	clone := cfg
	if clone.NodeRPCToken != "" {
		clone.NodeRPCToken = maskSecret(clone.NodeRPCToken)
	}
	if clone.SharedSecretValue != "" {
		clone.SharedSecretValue = maskSecret(clone.SharedSecretValue)
	}
	return clone
}

// Validate ensures the configuration is internally consistent.
func (cfg Config) Validate() error {
	if !cfg.AllowInsecure {
		if strings.TrimSpace(cfg.TLSCertFile) == "" || strings.TrimSpace(cfg.TLSKeyFile) == "" {
			return fmt.Errorf("tls credentials required unless allow_insecure is true")
		}
	}
	if cfg.MTLSRequired && strings.TrimSpace(cfg.TLSClientCAFile) == "" {
		return fmt.Errorf("mtls requires a client CA file")
	}
	if cfg.RateLimitPerMin < 0 {
		return fmt.Errorf("rate limit per minute must be non-negative")
	}
	return nil
}

func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	return "***"
}

func stringFromEnv(key, fallback string) string {
	trimmed := strings.TrimSpace(os.Getenv(key))
	if trimmed == "" {
		return fallback
	}
	return trimmed
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

func boolFromEnv(key string, fallback bool) bool {
	trimmed := strings.TrimSpace(os.Getenv(key))
	if trimmed == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(trimmed)
	if err != nil {
		return fallback
	}
	return parsed
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
