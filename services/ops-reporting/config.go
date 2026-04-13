package main

import (
	"fmt"
	"os"
	"strings"
)

// Config captures runtime configuration for the operator reporting service.
type Config struct {
	ListenAddress  string
	PaymentsDBPath string
	EscrowDBPath   string
	TreasuryDBPath string
	PayoutDBPath   string
	BearerToken    string
}

const (
	envOpsListen      = "OPS_REPORT_LISTEN"
	envOpsPaymentsDB  = "OPS_REPORT_PAYMENTS_DB"
	envOpsEscrowDB    = "OPS_REPORT_ESCROW_DB"
	envOpsTreasuryDB  = "OPS_REPORT_TREASURY_DB"
	envOpsPayoutDB    = "OPS_REPORT_PAYOUT_DB"
	envOpsBearerToken = "OPS_REPORT_BEARER_TOKEN"
)

// LoadConfigFromEnv resolves configuration from environment variables.
func LoadConfigFromEnv() (*Config, error) {
	cfg := &Config{
		ListenAddress:  getenvDefault(envOpsListen, ":8091"),
		PaymentsDBPath: getenvDefault(envOpsPaymentsDB, "payments-gateway.db"),
		EscrowDBPath:   getenvDefault(envOpsEscrowDB, "escrow-gateway.db"),
		TreasuryDBPath: getenvDefault(envOpsTreasuryDB, "nhb-data-local/payoutd/treasury.db"),
		PayoutDBPath:   getenvDefault(envOpsPayoutDB, "nhb-data-local/payoutd/executions.db"),
		BearerToken:    strings.TrimSpace(os.Getenv(envOpsBearerToken)),
	}
	if cfg.BearerToken == "" {
		return nil, fmt.Errorf("%s is required", envOpsBearerToken)
	}
	return cfg, nil
}

func getenvDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
