package core

import "math/big"

// Base-reward budget floor (ledger PL-CLM-02).
//
// The daily budget for base rewards is the smaller of a USD cap and a share of
// the last seven days of fees (nhbstate.CalcDailyBudgetZNHB). With next to no
// fees collected the second term is zero, the budget is zero and every reward is
// pro-rated to nothing, however small the day's demand. The only floor the code
// had (the price guard's fallback minimum emission) is written once at genesis,
// is not a governance parameter and is not limited by the USD cap.
//
// From loyaltyBudgetFloorFromHeight (zero = off, the live value) the day's
// budget is at least loyaltyBudgetFloorWei, limited to the existing daily USD cap
// (nhbstate.Manager.GetRemainingDailyBudgetZNHBWithFloor). The size of the floor
// is an owner decision: with the setting at zero or the amount empty nothing
// changes. The reward is still paid from the loyalty treasury, and the
// per-transaction, per-user and per-counterparty caps still apply. Only the
// pro-rated settlement (EndBlockRewards, the mode production enforces) uses the
// floor: the immediate path taken when pro-rating is switched off pays the whole
// reward and only reports the budget afterwards, so it keeps the plain figure.

// SetLoyaltyBudgetFloor sets the first block height from which the base-reward
// daily budget has a floor, and the floor in ZNHB wei. A zero height or a nil or
// non-positive amount leaves the budget as it is. A chain rule: the same values
// on every validator, chosen above the chain tip, all restarted together.
func (sp *StateProcessor) SetLoyaltyBudgetFloor(fromHeight uint64, floorWei *big.Int) {
	if sp == nil {
		return
	}
	sp.loyaltyBudgetFloorFromHeight = fromHeight
	if floorWei == nil || floorWei.Sign() <= 0 {
		sp.loyaltyBudgetFloorWei = nil
		return
	}
	sp.loyaltyBudgetFloorWei = new(big.Int).Set(floorWei)
}

// loyaltyBudgetFloorAtHeight returns the floor in force for a block at height,
// or nil when there is none.
func (sp *StateProcessor) loyaltyBudgetFloorAtHeight(height uint64) *big.Int {
	if sp == nil || sp.loyaltyBudgetFloorFromHeight == 0 || sp.loyaltyBudgetFloorWei == nil {
		return nil
	}
	if height < sp.loyaltyBudgetFloorFromHeight {
		return nil
	}
	return new(big.Int).Set(sp.loyaltyBudgetFloorWei)
}
