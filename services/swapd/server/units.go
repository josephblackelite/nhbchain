package server

import (
	"fmt"
	"math"
)

const stableAmountScale = 1_000_000

// ToStableAmountUnits converts a decimal amount into scaled integer units used by the stable engine.
func ToStableAmountUnits(amount float64) (int64, error) {
	if amount <= 0 {
		return 0, fmt.Errorf("amount must be positive")
	}
	scaled := math.Round(amount * float64(stableAmountScale))
	units := int64(scaled)
	if units <= 0 {
		return 0, fmt.Errorf("amount must be positive")
	}
	if !withinStableTolerance(amount, units) {
		return 0, fmt.Errorf("amount precision exceeds supported scale")
	}
	return units, nil
}

func withinStableTolerance(value float64, units int64) bool {
	recon := float64(units) / float64(stableAmountScale)
	diff := math.Abs(value - recon)
	tolerance := 1.0 / float64(stableAmountScale*10)
	return diff <= tolerance
}
