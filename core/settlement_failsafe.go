package core

// Two chain rules that keep an error in epoch settlement from stopping the block
// that closes the epoch (PL-EXT-28 and PL-EXT-29 item E-1b).
//
// An epoch is closed by the block at its last height, and finalizeEpoch returns
// any error of the settlement steps it runs to that block. A block that fails
// fails on every validator, and the only way to correct the cause is a
// transaction in a later block, so a cause that stops the epoch-closing block
// cannot be corrected on the chain. Two such causes are known:
//
//   - A governed buyback discount or safety margin of 10,000 basis points makes
//     the highest price the buyback may pay exactly zero. The price check of the
//     settlement then refuses it, with an error, as soon as one ask and one
//     signed reference price are filed for the epoch.
//   - The reward settlement debits the epoch's payout from the admin wallet's
//     liquid ZNHB balance and returns an error when the balance is lower. Staking
//     ZNHB from that wallet moves it out of the liquid balance.
//
// Both rules are off until their height (zero is off, the shipped and live
// value), and both are chain rules: the same value on every validator, chosen
// above the chain tip, all validators restarted together (see
// docs/consensus/invariants.md).

// SetBuybackPriceGuard sets the first block height from which governance refuses
// a buyback discount or safety margin that would make the highest buyback price
// zero, when a proposal is submitted and when one is executed. A zero height
// leaves governance as it was.
func (sp *StateProcessor) SetBuybackPriceGuard(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.buybackPriceGuardFromHeight = fromHeight
}

// SetEpochSettlementFailsafe sets the first block height from which an epoch
// settlement that cannot use the buyback price, or that finds the admin wallet's
// liquid ZNHB below the epoch's reward payout, degrades instead of returning an
// error. A zero height leaves the settlement as it was.
func (sp *StateProcessor) SetEpochSettlementFailsafe(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.epochSettlementFailsafeFromHeight = fromHeight
}

// epochSettlementFailsafeActive reports whether the epoch closed by the block at
// height settles with the failsafe. The height is the closing block's own, not
// the executing context's, so the answer is the same in every replay.
func (sp *StateProcessor) epochSettlementFailsafeActive(height uint64) bool {
	if sp == nil || sp.epochSettlementFailsafeFromHeight == 0 {
		return false
	}
	return height >= sp.epochSettlementFailsafeFromHeight
}
