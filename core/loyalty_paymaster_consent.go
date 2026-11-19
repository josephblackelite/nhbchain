package core

// PL-R5T-15: a loyalty program reward is paid only out of a wallet that is the
// business owner's own or has a standing record of consent, from a configured
// height.
//
// The consent rule of native/loyalty's Registry.SetPaymaster runs only when an
// assignment is made. A reward debits business.Paymaster whenever a spender pays the
// business's merchant, so an assignment already in state keeps paying although the
// wallet never consented, the treasury or admin wallet included (the Reward Pool is
// debited with it). From the height the reward path checks the standing record
// SetPaymaster now writes (see native/loyalty), the paymaster can withdraw itself and
// a registered merchant address can remove itself. Before the height every one of
// these is as it was.

// SetLoyaltyPaymasterConsent sets the first block height from which the standing
// paymaster-consent rule applies. Zero (the default) leaves loyalty as it was at
// every height. A chain rule: the same value on every validator, set before the
// chain reaches the height.
func (sp *StateProcessor) SetLoyaltyPaymasterConsent(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.loyaltyPaymasterConsentFromHeight = fromHeight
}

// loyaltyStandingConsentActive reports whether the standing rule applies to the
// block being executed.
func (sp *StateProcessor) loyaltyStandingConsentActive() bool {
	if sp == nil || sp.loyaltyPaymasterConsentFromHeight == 0 {
		return false
	}
	return sp.blockHeight() >= sp.loyaltyPaymasterConsentFromHeight
}
