package config

import (
	"fmt"
	"math/big"
)

// PaymasterLimits represents the parsed paymaster throttling limits.
type PaymasterLimits struct {
	MerchantDailyCapWei *big.Int
	DeviceDailyTxCap    uint64
	GlobalDailyCapWei   *big.Int
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
