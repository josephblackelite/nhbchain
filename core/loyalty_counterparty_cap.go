package core

import (
	"math/big"

	"nhbchain/native/loyalty"
)

// PL-EXT-17: a base-reward cap per pair of accounts, from a configured height.
//
// The loyalty base reward has always had a cap on how much one pair of accounts can
// earn per UTC day (DailyCapCounterparty, A to B and B to A sharing it), meant to stop
// two accounts of one owner passing the same NHB back and forth. The relaunch genesis
// stores none, so on the live chain it is off. From loyaltyCounterpartyCapFromHeight
// the reward path reads loyaltyCounterpartyCapWei in its place when the stored
// configuration sets none; a cap the stored configuration sets is left as it is, and
// the configuration the RPC reads is unchanged.
//
// What it does not do, and the tests say so (core/loyalty_counterparty_cap_test.go):
// NHB passed along a chain of fresh accounts still earns at every hop, because every
// hop is a new pair. Which mitigation covers that is the owner's decision (PL-EXT-17
// in the ledger); funding the reward (PL-CLM-02) stays blocked on it.

// SetLoyaltyCounterpartyCap sets the first block height from which the base-reward
// path applies a per-pair daily cap when the stored configuration sets none, and the
// cap in ZNHB wei. A zero height or a nil or non-positive amount leaves the rule as it
// was. A chain rule: the same values on every validator, set before the chain reaches
// the height.
func (sp *StateProcessor) SetLoyaltyCounterpartyCap(fromHeight uint64, capWei *big.Int) {
	if sp == nil {
		return
	}
	sp.loyaltyCounterpartyCapFromHeight = fromHeight
	if capWei == nil || capWei.Sign() <= 0 {
		sp.loyaltyCounterpartyCapWei = nil
		return
	}
	sp.loyaltyCounterpartyCapWei = new(big.Int).Set(capWei)
}

// loyaltyCounterpartyCapActive returns the per-pair cap in force for the block being
// executed, or nil when there is none.
func (sp *StateProcessor) loyaltyCounterpartyCapActive() *big.Int {
	if sp == nil || sp.loyaltyCounterpartyCapFromHeight == 0 || sp.loyaltyCounterpartyCapWei == nil {
		return nil
	}
	if sp.blockHeight() < sp.loyaltyCounterpartyCapFromHeight {
		return nil
	}
	return new(big.Int).Set(sp.loyaltyCounterpartyCapWei)
}

// LoyaltyGlobalConfig is the configuration the base-reward path reads. It is the
// stored one, with the per-pair cap filled in from the setting when the stored one
// has none and the height has been reached.
func (s loyaltyRewardState) LoyaltyGlobalConfig() (*loyalty.GlobalConfig, error) {
	cfg, err := s.StateProcessor.LoyaltyGlobalConfig()
	if err != nil || cfg == nil {
		return cfg, err
	}
	if capWei := s.StateProcessor.loyaltyCounterpartyCapActive(); capWei != nil {
		if cfg.DailyCapCounterparty == nil || cfg.DailyCapCounterparty.Sign() == 0 {
			// StateProcessor.LoyaltyGlobalConfig decodes a new value on every call, so
			// this changes the reward path's view and nothing stored or shared.
			cfg.DailyCapCounterparty = capWei
		}
	}
	return cfg, nil
}
