package config

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"nhbchain/crypto"
)

// PaymasterLimits represents the parsed paymaster throttling limits.
type PaymasterLimits struct {
	MerchantDailyCapWei *big.Int
	DeviceDailyTxCap    uint64
	GlobalDailyCapWei   *big.Int
}

// PaymasterAutoTopUpConfig represents the parsed automatic top-up policy values.
type PaymasterAutoTopUpConfig struct {
	Enabled        bool
	Token          string
	MinBalanceWei  *big.Int
	TopUpAmountWei *big.Int
	DailyCapWei    *big.Int
	Cooldown       time.Duration
	Operator       [20]byte
	ApproverRole   string
	MinterRole     string
}

// PaymasterLimits parses the configured paymaster throttling caps into runtime values.
func (g Global) PaymasterLimits() (PaymasterLimits, error) {
	limits := PaymasterLimits{DeviceDailyTxCap: g.Paymaster.DeviceDailyTxCap}
	merchantCap, err := parseUintAmount(g.Paymaster.MerchantDailyCapWei)
	if err != nil {
		return limits, fmt.Errorf("invalid global.paymaster.MerchantDailyCapWei: %w", err)
	}
	limits.MerchantDailyCapWei = merchantCap
	globalCap, err := parseUintAmount(g.Paymaster.GlobalDailyCapWei)
	if err != nil {
		return limits, fmt.Errorf("invalid global.paymaster.GlobalDailyCapWei: %w", err)
	}
	limits.GlobalDailyCapWei = globalCap
	return limits, nil
}

// PaymasterAutoTopUpConfig parses the automatic top-up policy into runtime values.
func (g Global) PaymasterAutoTopUpConfig() (PaymasterAutoTopUpConfig, error) {
	cfg := PaymasterAutoTopUpConfig{Enabled: g.Paymaster.AutoTopUp.Enabled}
	token := strings.ToUpper(strings.TrimSpace(g.Paymaster.AutoTopUp.Token))
	if token == "" {
		token = "ZNHB"
	}
	if token != "ZNHB" {
		return cfg, fmt.Errorf("invalid global.paymaster.AutoTopUp.Token: must be ZNHB, got %q", g.Paymaster.AutoTopUp.Token)
	}
	cfg.Token = token

	minBalance, err := parseUintAmount(g.Paymaster.AutoTopUp.MinBalanceWei)
	if err != nil {
		return cfg, fmt.Errorf("invalid global.paymaster.AutoTopUp.MinBalanceWei: %w", err)
	}
	cfg.MinBalanceWei = minBalance

	topUpAmount, err := parseUintAmount(g.Paymaster.AutoTopUp.TopUpAmountWei)
	if err != nil {
		return cfg, fmt.Errorf("invalid global.paymaster.AutoTopUp.TopUpAmountWei: %w", err)
	}
	cfg.TopUpAmountWei = topUpAmount

	dailyCap, err := parseUintAmount(g.Paymaster.AutoTopUp.DailyCapWei)
	if err != nil {
		return cfg, fmt.Errorf("invalid global.paymaster.AutoTopUp.DailyCapWei: %w", err)
	}
	cfg.DailyCapWei = dailyCap

	if g.Paymaster.AutoTopUp.CooldownSeconds > 0 {
		cfg.Cooldown = time.Duration(g.Paymaster.AutoTopUp.CooldownSeconds) * time.Second
	}

	operatorRef := strings.TrimSpace(g.Paymaster.AutoTopUp.Governance.Operator)
	if operatorRef != "" {
		addr, err := crypto.DecodeAddress(operatorRef)
		if err != nil {
			return cfg, fmt.Errorf("invalid global.paymaster.AutoTopUp.Governance.Operator: %w", err)
		}
		copy(cfg.Operator[:], addr.Bytes())
	}

	cfg.ApproverRole = strings.TrimSpace(g.Paymaster.AutoTopUp.Governance.ApproverRole)
	cfg.MinterRole = strings.TrimSpace(g.Paymaster.AutoTopUp.Governance.MinterRole)

	return cfg, nil
}

// TransferZNHBPaused reports whether direct ZNHB transfers are currently disabled via governance.
func (g Global) TransferZNHBPaused() bool {
	return g.Pauses.TransferZNHB
}
