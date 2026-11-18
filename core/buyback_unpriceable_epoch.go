package core

// PL-EXT-28: an epoch whose buyback price cannot be computed refunds its asks in full,
// from a configured height, instead of failing the block that closes the epoch.
//
// The buyback's highest acceptable price is the lesser of the curve price and the signed
// reference price, cut by the governed discount and again by the governed safety margin.
// Both governed values may be set up to 10,000 basis points, the whole price, and at
// exactly 10,000 the highest price is zero: MaxBuybackPrice returns it without an error,
// the fill step refuses it with "price must be positive", settleBuybackEpoch returned that
// error, and the block that closes the epoch is rejected by every validator that applies
// it. The epoch can never close, so the chain
// stops at that block until the parameter is changed, which needs a block. A passed
// governance proposal is all it takes; the proposal builder of the portal now refuses
// the value, but the chain accepted it.
//
// From the height an epoch the engine cannot price is settled like an epoch with no
// reference price: nothing is bought and every seller gets the escrowed ZNHB back.

// SetBuybackUnpriceableEpochRefund sets the first block height from which an epoch whose
// highest buyback price cannot be computed, or is not positive, refunds every ask in full
// instead of failing the block. Zero (the default) keeps the error at every height. A chain rule: the same
// value on every validator, set before the chain reaches the height.
func (sp *StateProcessor) SetBuybackUnpriceableEpochRefund(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.buybackUnpriceableRefundFromHeight = fromHeight
}

// buybackUnpriceableEpochRefunds reports whether the epoch being settled is refunded, not
// failed, when its highest price cannot be computed.
func (sp *StateProcessor) buybackUnpriceableEpochRefunds() bool {
	return sp != nil && sp.buybackUnpriceableRefundFromHeight != 0 && sp.blockHeight() >= sp.buybackUnpriceableRefundFromHeight
}
