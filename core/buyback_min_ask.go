package core

import "math/big"

// PL-R5T-33: a larger smallest-ask, from a configured height.
//
// An epoch's pending asks are bounded at 1,024 and one address's at 8, and an ask
// costs only ZNHB that is refunded or filled at the settlement, so about 128
// addresses with a few ZNHB each can fill every slot and refuse honest sellers for
// the epoch. From buybackMinAskFromHeight the smallest ask is buybackMinAskWei when
// that is more than the fixed one ZNHB: filling every slot then locks that many
// times more ZNHB. The limits themselves are unchanged (letting a larger ask evict the
// smallest is the other option and is not built).

// SetBuybackMinAsk sets the first block height from which the smallest buyback ask is
// minAskWei, if that is above the fixed minimum of one ZNHB. A zero height or a nil or
// non-positive amount leaves the minimum as it was. A chain rule: the same values on
// every validator, set before the chain reaches the height.
func (sp *StateProcessor) SetBuybackMinAsk(fromHeight uint64, minAskWei *big.Int) {
	if sp == nil {
		return
	}
	sp.buybackMinAskFromHeight = fromHeight
	if minAskWei == nil || minAskWei.Sign() <= 0 {
		sp.buybackMinAskWei = nil
		return
	}
	sp.buybackMinAskWei = new(big.Int).Set(minAskWei)
}

// minBuybackAsk returns the smallest ask the block being executed accepts: the fixed
// one ZNHB, or the configured amount from its height when that is larger.
func (sp *StateProcessor) minBuybackAsk() *big.Int {
	min := new(big.Int).Set(minBuybackAskWei)
	if sp == nil || sp.buybackMinAskFromHeight == 0 || sp.buybackMinAskWei == nil {
		return min
	}
	if sp.blockHeight() >= sp.buybackMinAskFromHeight && sp.buybackMinAskWei.Cmp(min) > 0 {
		return new(big.Int).Set(sp.buybackMinAskWei)
	}
	return min
}
