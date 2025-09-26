package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config represents runtime configuration for the OTC gateway service.
type Config struct {
	Port        string
	DatabaseURL string
	S3Bucket    string
	ChainID     string
	RPCBase     string
	DefaultTZ   *time.Location
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

	rpcBase := os.Getenv("NHB_RPC_BASE")
	if rpcBase == "" {
		return nil, fmt.Errorf("NHB_RPC_BASE is required")
	}

	tzName := getEnvDefault("OTC_TZ_DEFAULT", "UTC")
	tz, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("invalid OTC_TZ_DEFAULT %q: %w", tzName, err)
	}

	return &Config{
		Port:        normalizePort(port),
		DatabaseURL: dbURL,
		S3Bucket:    bucket,
		ChainID:     chainID,
		RPCBase:     rpcBase,
		DefaultTZ:   tz,
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
