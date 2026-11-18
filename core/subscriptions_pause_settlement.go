package core

import nativecommon "nhbchain/native/common"

// PL-R5T-81: the subscription settlement does nothing while the module is paused,
// from a configured height.
//
// Pausing the subscriptions module refuses every subscriptions transaction, a
// payer's cancel included, but settleSubscriptionCharges runs every block and never
// looked at the pause, so payers kept being charged each cycle while unable to
// cancel. From subsPauseSettleFromHeight a paused module is not
// settled: nothing is charged, no failed attempt is recorded and no watermark moves,
// so the days skipped are settled again on unpause (one catch-up charge per due
// subscription; the due entries stay where they are). Lending's choice, to count a
// paused cycle as a missed payment, is deliberately not copied.

// SetSubscriptionsPauseSettlement sets the first block height from which the per-block
// subscription settlement does nothing while the subscriptions module is paused. Zero
// (the default) leaves it running through a pause at every height. A chain rule: the
// same value on every validator, set before the chain reaches the height.
func (sp *StateProcessor) SetSubscriptionsPauseSettlement(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.subsPauseSettleFromHeight = fromHeight
}

// subscriptionsSettlementPaused reports whether the settlement of the block being
// executed must be skipped because the subscriptions module is paused.
func (sp *StateProcessor) subscriptionsSettlementPaused() bool {
	if sp == nil || sp.subsPauseSettleFromHeight == 0 || sp.blockHeight() < sp.subsPauseSettleFromHeight {
		return false
	}
	return nativecommon.Guard(sp.pauses, "subscriptions") != nil
}
