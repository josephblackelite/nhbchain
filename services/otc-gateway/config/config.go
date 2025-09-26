package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config represents runtime configuration for the OTC gateway service.
type Config struct {
	Port             string
	DatabaseURL      string
	S3Bucket         string
	ChainID          string
	SwapRPCBase      string
	DefaultTZ        *time.Location
	HSMBaseURL       string
	HSMCACert        string
	HSMClientCert    string
	HSMClientKey     string
	HSMKeyLabel      string
	HSMOverrideDN    string
	SwapProvider     string
	VoucherTTL       time.Duration
	MintPollInterval time.Duration
	ReconOutputDir   string
	ReconRunHour     int
	ReconRunMinute   int
	ReconDryRun      bool
	ReconWindow      time.Duration
}

// FromEnv loads configuration from environment variables required by the service.
func FromEnv() (*Config, error) {
	port := getEnvDefault("OTC_PORT", "8080")
	dbURL := os.Getenv("OTC_DB_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("OTC_DB_URL is required")
	}

	bucket := os.Getenv("OTC_S3_BUCKET")
	if bucket == "" {
		return nil, fmt.Errorf("OTC_S3_BUCKET is required")
	}

	chainID := os.Getenv("OTC_CHAIN_ID")
	if chainID == "" {
		return nil, fmt.Errorf("OTC_CHAIN_ID is required")
	}

	rpcBase := os.Getenv("OTC_SWAP_RPC_BASE")
	if rpcBase == "" {
		rpcBase = os.Getenv("NHB_RPC_BASE")
	}
	if rpcBase == "" {
		return nil, fmt.Errorf("OTC_SWAP_RPC_BASE is required")
	}

	hsmBase := os.Getenv("OTC_HSM_BASE_URL")
	if hsmBase == "" {
		return nil, fmt.Errorf("OTC_HSM_BASE_URL is required")
	}
	hsmCACert := os.Getenv("OTC_HSM_CA_CERT")
	if hsmCACert == "" {
		return nil, fmt.Errorf("OTC_HSM_CA_CERT is required")
	}
	hsmClientCert := os.Getenv("OTC_HSM_CLIENT_CERT")
	if hsmClientCert == "" {
		return nil, fmt.Errorf("OTC_HSM_CLIENT_CERT is required")
	}
	hsmClientKey := os.Getenv("OTC_HSM_CLIENT_KEY")
	if hsmClientKey == "" {
		return nil, fmt.Errorf("OTC_HSM_CLIENT_KEY is required")
	}
	hsmKeyLabel := getEnvDefault("OTC_HSM_KEY_LABEL", "MINTER_NHB")
	swapProvider := getEnvDefault("OTC_SWAP_PROVIDER", "otc-gateway")

	ttlSeconds := getEnvDefault("OTC_VOUCHER_TTL_SECONDS", "900")
	ttl, err := strconv.Atoi(ttlSeconds)
	if err != nil || ttl <= 0 {
		return nil, fmt.Errorf("invalid OTC_VOUCHER_TTL_SECONDS %q", ttlSeconds)
	}

	pollSeconds := getEnvDefault("OTC_MINT_POLL_INTERVAL_SECONDS", "10")
	poll, err := strconv.Atoi(pollSeconds)
	if err != nil || poll <= 0 {
		return nil, fmt.Errorf("invalid OTC_MINT_POLL_INTERVAL_SECONDS %q", pollSeconds)
	}

	tzName := getEnvDefault("OTC_TZ_DEFAULT", "UTC")
	tz, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("invalid OTC_TZ_DEFAULT %q: %w", tzName, err)
	}

	reconDir := getEnvDefault("OTC_RECON_OUTPUT_DIR", "nhb-data-local/recon")
	reconHour := parseIntEnv("OTC_RECON_RUN_HOUR", 1)
	reconMinute := parseIntEnv("OTC_RECON_RUN_MINUTE", 5)
	reconDryRun := parseBoolEnv("OTC_RECON_DRY_RUN", false)
	windowHours := parseIntEnv("OTC_RECON_WINDOW_HOURS", 24)
	reconWindow := time.Duration(windowHours) * time.Hour

	return &Config{
		Port:             normalizePort(port),
		DatabaseURL:      dbURL,
		S3Bucket:         bucket,
		ChainID:          chainID,
		SwapRPCBase:      rpcBase,
		DefaultTZ:        tz,
		HSMBaseURL:       hsmBase,
		HSMCACert:        hsmCACert,
		HSMClientCert:    hsmClientCert,
		HSMClientKey:     hsmClientKey,
		HSMKeyLabel:      hsmKeyLabel,
		HSMOverrideDN:    os.Getenv("OTC_HSM_SIGNER_DN"),
		SwapProvider:     swapProvider,
		VoucherTTL:       time.Duration(ttl) * time.Second,
		MintPollInterval: time.Duration(poll) * time.Second,
		ReconOutputDir:   reconDir,
		ReconRunHour:     reconHour,
		ReconRunMinute:   reconMinute,
		ReconDryRun:      reconDryRun,
		ReconWindow:      reconWindow,
	}, nil
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func normalizePort(port string) string {
	if port == "" {
		return "8080"
	}
	if _, err := strconv.Atoi(port); err == nil {
		return port
	}
	// Allow values like ":8080".
	if len(port) > 0 && port[0] == ':' {
		return port[1:]
	}
	return port
}

func parseIntEnv(key string, def int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return def
}

func parseBoolEnv(key string, def bool) bool {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return def
}
