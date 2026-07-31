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
	// FeeBps is the protocol-enforced fee, in basis points of the transfer
	// amount, charged once the sender exceeds FreeSpendLimitWei --
	// replacing the sender's self-declared GasPrice*GasLimit. A percentage
	// rather than a flat fee so it scales with value moved. See
	// docs/issue30.md item 7b.
	FeeBps uint32
}

func (p TransferGasPolicy) Clone() TransferGasPolicy {
	clone := TransferGasPolicy{
		Enabled:      p.Enabled,
		Window:       normalizeTransferGasWindow(p.Window),
		FeeCollector: p.FeeCollector,
		FeeBps:       p.FeeBps,
	}
	if p.FreeSpendLimitWei != nil {
		clone.FreeSpendLimitWei = new(big.Int).Set(p.FreeSpendLimitWei)
	} else {
		clone.FreeSpendLimitWei = big.NewInt(0)
	}
	return clone
}

// ComputeFee computes the protocol-enforced fee for a transfer of the given
// value, using FeeBps. Returns 0 if value is nil/non-positive or FeeBps is 0.
func (p TransferGasPolicy) ComputeFee(value *big.Int) *big.Int {
	if value == nil || value.Sign() <= 0 || p.FeeBps == 0 {
		return big.NewInt(0)
	}
	fee := new(big.Int).Mul(value, new(big.Int).SetUint64(uint64(p.FeeBps)))
	return fee.Quo(fee, big.NewInt(10_000))
}

func normalizeTransferGasWindow(window string) string {
	switch strings.ToLower(strings.TrimSpace(window)) {
	case TransferGasWindowMonthly:
		return TransferGasWindowMonthly
	default:
		return TransferGasWindowLifetime
	}
}
