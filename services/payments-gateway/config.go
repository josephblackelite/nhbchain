package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config captures runtime configuration for the payments gateway service.
type Config struct {
	ListenAddress        string
	DatabasePath         string
	NodeURL              string
	NodeAuthToken        string
	QuoteTTL             time.Duration
	QuoteCurrency        string
	OracleTTL            time.Duration
	OracleMaxDeviation   float64
	OracleCircuitBreaker float64
	NowPaymentsAPIKey    string
	NowPaymentsIPNSecret string
	NowPaymentsBaseURL   string
	MinterKMSEnv         string
}

const (
	envListen          = "PAY_GATEWAY_LISTEN"
	envDBPath          = "PAY_GATEWAY_DB"
	envNodeURL         = "PAY_GATEWAY_NODE_URL"
	envNodeToken       = "PAY_GATEWAY_NODE_TOKEN"
	envQuoteTTL        = "PAY_GATEWAY_QUOTE_TTL"
	envOracleTTL       = "PAY_GATEWAY_ORACLE_TTL"
	envOracleDeviation = "PAY_GATEWAY_ORACLE_DEVIATION"
	envOracleBreaker   = "PAY_GATEWAY_ORACLE_BREAKER"
	envNowAPIKey       = "PAY_GATEWAY_NOW_API_KEY"
	envNowIPNSecret    = "PAY_GATEWAY_NOW_IPN_SECRET"
	envNowBaseURL      = "PAY_GATEWAY_NOW_BASE"
	envKMSEnv          = "PAY_GATEWAY_MINTER_KMS_ENV"
)

// LoadConfigFromEnv resolves configuration from environment variables with sane defaults.
func LoadConfigFromEnv() (*Config, error) {
	cfg := &Config{
		ListenAddress:        getenvDefault(envListen, ":8080"),
		DatabasePath:         getenvDefault(envDBPath, "payments-gateway.db"),
		NodeURL:              os.Getenv(envNodeURL),
		NodeAuthToken:        os.Getenv(envNodeToken),
		QuoteTTL:             parseDurationDefault(envQuoteTTL, 5*time.Minute),
		QuoteCurrency:        "USD",
		OracleTTL:            parseDurationDefault(envOracleTTL, time.Minute),
		OracleMaxDeviation:   parsePercentDefault(envOracleDeviation, 0.05),
		OracleCircuitBreaker: parsePercentDefault(envOracleBreaker, 0.20),
		NowPaymentsAPIKey:    os.Getenv(envNowAPIKey),
		NowPaymentsIPNSecret: os.Getenv(envNowIPNSecret),
		NowPaymentsBaseURL:   getenvDefault(envNowBaseURL, "https://api.nowpayments.io/v1"),
		MinterKMSEnv:         os.Getenv(envKMSEnv),
	}

	if cfg.NodeURL == "" {
		return nil, fmt.Errorf("%s is required", envNodeURL)
	}
	if cfg.NowPaymentsAPIKey == "" {
		return nil, fmt.Errorf("%s is required", envNowAPIKey)
	}
	if cfg.NowPaymentsIPNSecret == "" {
		return nil, fmt.Errorf("%s is required", envNowIPNSecret)
	}
	if cfg.MinterKMSEnv == "" {
		return nil, fmt.Errorf("%s is required", envKMSEnv)
	}

	return cfg, nil
}

func getenvDefault(key, def string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return def
}

func parseDurationDefault(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return def
	}
	return d
}

func parsePercentDefault(key string, def float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	if f < 0 {
		f = 0
	}
	return f
}
