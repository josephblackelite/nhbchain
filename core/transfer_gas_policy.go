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
	// amount, charged on an NHB transfer once the sender exceeds
	// FreeSpendLimitWei -- replacing the sender's self-declared
	// GasPrice*GasLimit. A percentage rather than a flat fee so it scales
	// with value moved. See docs/issue30.md item 7b.
	FeeBps uint32
	// FeeBpsZNHB is FeeBps' counterpart for ZNHB transfers -- a separate,
	// independently configurable rate rather than a reuse of FeeBps, since
	// ZNHB is priced differently from NHB and was never deliberately meant
	// to share NHB's rate. Applies once the sender exceeds
	// FreeSpendLimitWei on a ZNHB transfer, via the same asset-scoped
	// free-tier tracking as NHB.
	FeeBpsZNHB uint32
}

func (p TransferGasPolicy) Clone() TransferGasPolicy {
	clone := TransferGasPolicy{
		Enabled:      p.Enabled,
		Window:       normalizeTransferGasWindow(p.Window),
		FeeCollector: p.FeeCollector,
		FeeBps:       p.FeeBps,
		FeeBpsZNHB:   p.FeeBpsZNHB,
	}
	if p.FreeSpendLimitWei != nil {
		clone.FreeSpendLimitWei = new(big.Int).Set(p.FreeSpendLimitWei)
	} else {
		clone.FreeSpendLimitWei = big.NewInt(0)
	}
	return clone
}

// FeeBpsForAsset returns the configured protocol-enforced fee rate, in basis
// points, for the given asset ("NHB" or "ZNHB", case-insensitive). Any other
// value (including empty) falls back to the NHB rate, matching ComputeFee's
// default behavior.
func (p TransferGasPolicy) FeeBpsForAsset(asset string) uint32 {
	if strings.EqualFold(strings.TrimSpace(asset), "ZNHB") {
		return p.FeeBpsZNHB
	}
	return p.FeeBps
}

// ComputeFee computes the protocol-enforced fee for a transfer of the given
// value in the given asset ("NHB" or "ZNHB", case-insensitive; see
// FeeBpsForAsset), using that asset's own rate -- FeeBps for NHB, FeeBpsZNHB
// for ZNHB. Returns 0 if value is nil/non-positive or the resolved rate is 0.
func (p TransferGasPolicy) ComputeFee(asset string, value *big.Int) *big.Int {
	if value == nil || value.Sign() <= 0 {
		return big.NewInt(0)
	}
	bps := p.FeeBpsForAsset(asset)
	if bps == 0 {
		return big.NewInt(0)
	}
	fee := new(big.Int).Mul(value, new(big.Int).SetUint64(uint64(bps)))
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
