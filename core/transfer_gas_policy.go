package core

import (
	"math/big"
	"strings"
)

const (
	TransferGasWindowLifetime = "lifetime"
	TransferGasWindowMonthly  = "monthly"
)

// TransferGasPolicy controls when NHB transfer gas is waived and where charged
// gas is routed once the free tier is exhausted.
type TransferGasPolicy struct {
	Enabled           bool
	FreeSpendLimitWei *big.Int
	Window            string
	FeeCollector      [20]byte
}

func (p TransferGasPolicy) Clone() TransferGasPolicy {
	clone := TransferGasPolicy{
		Enabled:      p.Enabled,
		Window:       normalizeTransferGasWindow(p.Window),
		FeeCollector: p.FeeCollector,
	}
	if p.FreeSpendLimitWei != nil {
		clone.FreeSpendLimitWei = new(big.Int).Set(p.FreeSpendLimitWei)
	} else {
		clone.FreeSpendLimitWei = big.NewInt(0)
	}
	return clone
}

func normalizeTransferGasWindow(window string) string {
	switch strings.ToLower(strings.TrimSpace(window)) {
	case TransferGasWindowMonthly:
		return TransferGasWindowMonthly
	default:
		return TransferGasWindowLifetime
	}
}
