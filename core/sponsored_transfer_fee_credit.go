package core

import "math/big"

// PL-EXT-38: the fee a paymaster pays for a sponsored NHB transfer is credited to the
// fee collector whenever one was debited, from a configured height.
//
// The sponsored branch of the NHB transfer debits the sponsor the protocol fee and then
// credited the fee collector only inside `if transferGasPolicy.Enabled`. The policy is
// built disabled whenever its free-tier spend limit is zero, whatever the fee rate, so
// with a zero free tier and a fee rate above zero every sponsored NHB transfer took the
// fee from the sponsor and credited it to nobody: destroyed, not collected. That is the
// shape PL-DC-08 closed in the sender's own branch, which now credits on a positive
// debited fee; this is the same rule for the sponsor's branch, behind a height because it
// changes the balances a block leaves behind.

// SetSponsoredTransferFeeCredit sets the first block height from which the fee debited
// from a sponsor for a sponsored NHB transfer is always credited to the transfer fee
// collector. Zero (the default) leaves the credit tied to the transfer gas policy being
// enabled at every height. A chain rule: the same value on every validator, set before
// the chain reaches the height.
func (sp *StateProcessor) SetSponsoredTransferFeeCredit(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.sponsoredFeeCreditFromHeight = fromHeight
}

// sponsoredTransferFeeCredited reports whether the fee taken from a sponsor is credited.
// With the transfer gas policy enabled it always was, and still is, whatever the height
// (the rule changes nothing there). With the policy disabled it is credited from the
// configured height when the fee is positive; below the height, or with the setting at
// zero, it is not (the older behaviour).
func (sp *StateProcessor) sponsoredTransferFeeCredited(policyEnabled bool, gasCost *big.Int) bool {
	if policyEnabled {
		return true
	}
	if sp == nil || sp.sponsoredFeeCreditFromHeight == 0 || sp.blockHeight() < sp.sponsoredFeeCreditFromHeight {
		return false
	}
	return gasCost != nil && gasCost.Sign() > 0
}
