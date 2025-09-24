package engagement

import (
	"fmt"
	"time"
)

// Config controls how engagement scores are calculated and validated.
type Config struct {
	HeartbeatWeight        uint64        // Weight applied to minutes online
	TxWeight               uint64        // Weight applied to general transactions
	EscrowWeight           uint64        // Weight applied to escrow participation
	GovWeight              uint64        // Weight applied to governance activity
	DailyCap               uint64        // Maximum raw score credit per day
	LambdaNumerator        uint64        // Numerator for EMA decay factor (lambda)
	LambdaDenominator      uint64        // Denominator for EMA decay factor
	HeartbeatInterval      time.Duration // Minimum interval between heartbeats
	MaxMinutesPerHeartbeat uint64        // Clamp for minutes accrued per heartbeat
}

// DefaultConfig returns a conservative engagement configuration suitable for
// tests and development networks.
func DefaultConfig() Config {
	return Config{
		HeartbeatWeight:        1,
		TxWeight:               5,
		EscrowWeight:           10,
		GovWeight:              20,
		DailyCap:               250,
		LambdaNumerator:        4,
		LambdaDenominator:      5,
		HeartbeatInterval:      time.Minute,
		MaxMinutesPerHeartbeat: 5,
	}
}

// Validate ensures the configuration is internally consistent.
func (c Config) Validate() error {
	if c.LambdaDenominator == 0 {
		return fmt.Errorf("engagement lambda denominator must be non-zero")
	}
	if c.LambdaNumerator > c.LambdaDenominator {
		return fmt.Errorf("engagement lambda numerator must be <= denominator")
	}
	if c.HeartbeatInterval <= 0 {
		return fmt.Errorf("heartbeat interval must be positive")
	}
	if c.MaxMinutesPerHeartbeat == 0 {
		return fmt.Errorf("max minutes per heartbeat must be positive")
	}
	return nil
}
