package core

import (
	nhbstate "nhbchain/core/state"
)

// Monthly free tier for plain transfers (ledger PL-CLM-02).
//
// Today a plain transfer is free only while the sender's tracked spend in the
// asset is below TransferFreeTierSpendWei, over the sender's lifetime: one
// large transfer can cross the limit for nothing, and after that every
// transfer pays TransferFeeBps (NHB) or TransferFeeBpsZNHB (ZNHB) of its
// amount. Nothing in it is a count of transfers, so the public statement "your
// first 100 transactions each month are free" describes the merchant
// point-of-sale fee domain (FreeTierTxPerMonth), not ordinary transfers.
//
// From transferMonthlyFreeTierFromHeight (zero = off, the live value) a sender
// whose plain-transfer count in the current UTC calendar month is below
// transferMonthlyFreeTierTxPerMonth pays no transfer fee, in NHB or ZNHB. It is
// added to the existing rule, never taken from it: a transfer the spend
// allowance already waives stays free, and the count still includes it. Only the
// fee waiver changes; the transfer, its balances and the loyalty reward are
// untouched. A transfer carried by a paymaster is not counted (its fee is the
// paymaster's, not the sender's).

// SetTransferMonthlyFreeTier sets the first block height from which the monthly
// free tier is in force and the number of plain transfers per sender per UTC
// calendar month it waives. Zero for either leaves the tier off. A chain rule:
// the same values on every validator, chosen above the chain tip, all restarted
// together.
func (sp *StateProcessor) SetTransferMonthlyFreeTier(fromHeight, txPerMonth uint64) {
	if sp == nil {
		return
	}
	sp.transferMonthlyFreeTierFromHeight = fromHeight
	sp.transferMonthlyFreeTierTxPerMonth = txPerMonth
}

// transferMonthlyFreeTierActive reports whether the tier applies to the block
// being executed.
func (sp *StateProcessor) transferMonthlyFreeTierActive() bool {
	if sp == nil || sp.transferMonthlyFreeTierFromHeight == 0 || sp.transferMonthlyFreeTierTxPerMonth == 0 {
		return false
	}
	return sp.blockHeight() >= sp.transferMonthlyFreeTierFromHeight
}

// transferMonthlyFreeTierEligible reports whether the sender is still inside the
// monthly count, before the transfer being executed is counted.
func (sp *StateProcessor) transferMonthlyFreeTierEligible(sender []byte) (bool, error) {
	if !sp.transferMonthlyFreeTierActive() {
		return false, nil
	}
	var wallet [20]byte
	copy(wallet[:], sender)
	count, err := nhbstate.NewManager(sp.Trie).TransferGasMonthlyTxCount(wallet, sp.blockTimestamp())
	if err != nil {
		return false, err
	}
	return count < sp.transferMonthlyFreeTierTxPerMonth, nil
}

// recordTransferMonthlyFreeTierUse counts the transfer just executed.
func (sp *StateProcessor) recordTransferMonthlyFreeTierUse(sender []byte) error {
	if !sp.transferMonthlyFreeTierActive() {
		return nil
	}
	var wallet [20]byte
	copy(wallet[:], sender)
	_, err := nhbstate.NewManager(sp.Trie).TransferGasMonthlyTxCountAdd(wallet, sp.blockTimestamp())
	return err
}
