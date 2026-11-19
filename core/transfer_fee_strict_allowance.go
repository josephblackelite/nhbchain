package core

import (
	"math/big"

	nhbstate "nhbchain/core/state"
)

// W11 (second part): a strict free allowance.
//
// Until TransferFeeStrictAllowanceActivationHeight a transfer is free in full while the sender's recorded
// spend in the asset is below the allowance, measured BEFORE the transfer: the transfer that crosses the line
// is free in full, so a wallet whose first transfer is 1,000,000 NHB pays nothing on it. From the height the
// allowance is strict: the free part of a transfer is what is left of the allowance (the limit less the spend
// so far, never below zero, never more than the amount) and the fee is the asset's rate on the rest.
//
//	free       = min(max(0, limit - spent), amount)
//	chargeable = amount - free
//	fee        = chargeable * bps / 10,000   (rounded down, exact integers)
//
// It applies to the ordinary fee of both assets, so to a transfer that names a fee domain under the uniform
// rule, and to a governed allowance: the limit is whichever the block's effective policy carries. It does not
// change what is recorded as spent (the whole amount, as before), the window, the collector, the monthly free
// tier (which, where it applies, still waives the whole remaining fee) or a sponsored transfer (its fee is the
// sponsor's, on the whole amount, and the sender's allowance is not consulted, as before).

// SetTransferFeeStrictAllowance sets the first block height from which the free allowance is strict. Zero, the
// default, leaves the whole-transfer rule at every height. A chain rule: the same value on every validator, set
// before the chain reaches the height.
func (sp *StateProcessor) SetTransferFeeStrictAllowance(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.strictAllowanceFromHeight = fromHeight
}

func (sp *StateProcessor) strictAllowanceActive() bool {
	return sp != nil && sp.strictAllowanceFromHeight != 0 && sp.blockHeight() >= sp.strictAllowanceFromHeight
}

// FeeWithAllowance splits a transfer of amount into the part the allowance covers and the rest, and returns the
// fee on the rest, for a sender with remaining allowance left (zero when the policy has no allowance). With strict
// false the allowance is the whole-transfer rule: any remaining allowance waives the whole fee. All arithmetic is
// on big.Int; the inputs are never modified and the results are new values.
func (p TransferGasPolicy) FeeWithAllowance(asset string, amount, remaining *big.Int, strict bool) (free, chargeable, fee *big.Int) {
	if amount == nil || amount.Sign() <= 0 {
		return new(big.Int), new(big.Int), new(big.Int)
	}
	left := new(big.Int)
	if remaining != nil && remaining.Sign() > 0 {
		left.Set(remaining)
	}
	if !strict {
		if left.Sign() > 0 {
			return new(big.Int).Set(amount), new(big.Int), new(big.Int)
		}
		return new(big.Int), new(big.Int).Set(amount), p.ComputeFee(asset, amount)
	}
	free = new(big.Int).Set(amount)
	if left.Cmp(amount) < 0 {
		free.Set(left)
	}
	chargeable = new(big.Int).Sub(amount, free)
	return free, chargeable, p.ComputeFee(asset, chargeable)
}

// TransferFeeQuote is what a transfer of an amount would cost the wallet in the next block.
type TransferFeeQuote struct {
	// Eligible is true when the fee is zero because the allowance covers the whole transfer.
	Eligible        bool
	FreeWei         *big.Int
	ChargeableWei   *big.Int
	FeeWei          *big.Int
	FeeBps          uint32
	StrictAllowance bool
	Status          nhbstate.TransferGasSpendStatus
}

// TransferFeeStrictAllowanceNextBlock reports whether the strict allowance governs the next block, and the
// configured activation height.
func (n *Node) TransferFeeStrictAllowanceNextBlock() (active bool, height uint64) {
	if n == nil {
		return false, 0
	}
	next := n.GetHeight() + 1
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	height = n.transferFeeStrictAllowanceHeight
	return height != 0 && next >= height, height
}

// TransferFeeQuote prices a transfer of amount of asset ("NHB" or "ZNHB") by the wallet in the next block, from the
// policy and the allowance rule that block will use. It reads state and changes nothing.
func (n *Node) TransferFeeQuote(addr []byte, asset string, amount *big.Int) (TransferFeeQuote, error) {
	policy, err := n.EffectiveTransferGasPolicy()
	if err != nil {
		return TransferFeeQuote{}, err
	}
	status, err := n.TransferGasStatusForAsset(addr, asset)
	if err != nil {
		return TransferFeeQuote{}, err
	}
	strict, _ := n.TransferFeeStrictAllowanceNextBlock()
	remaining := new(big.Int)
	if policy.Enabled && status.Remaining != nil {
		remaining.Set(status.Remaining)
	}
	free, chargeable, fee := policy.FeeWithAllowance(asset, amount, remaining, strict)
	return TransferFeeQuote{
		Eligible:        remaining.Sign() > 0 && chargeable.Sign() == 0,
		FreeWei:         free,
		ChargeableWei:   chargeable,
		FeeWei:          fee,
		FeeBps:          policy.FeeBpsForAsset(asset),
		StrictAllowance: strict,
		Status:          status,
	}, nil
}

// SetTransferFeeStrictAllowance sets the first block height of the TransferFeeStrictAllowanceActivationHeight rule
// (see StateProcessor.SetTransferFeeStrictAllowance). Zero, the default, leaves it off. A chain rule: the same value
// on every validator, set before the chain reaches the height.
func (n *Node) SetTransferFeeStrictAllowance(height uint64) {
	if n == nil {
		return
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	n.transferFeeStrictAllowanceHeight = height
	if n.state != nil {
		n.state.SetTransferFeeStrictAllowance(height)
	}
}
