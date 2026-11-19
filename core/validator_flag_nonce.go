package core

import "nhbchain/core/types"

// A validator register or deregister that carries no value and the nonce
// (ledger PL-R5T-91).
//
// A stake transaction whose payload registers the sender as a validator
// candidate, with a zero value, and an unstake transaction whose payload
// deregisters it, with a zero value, only flip a flag (applyStake and
// applyUnstake). Only the value-bearing paths (StakeDelegate, StakeUndelegate)
// advance the sender's nonce, so these two never did: the signed bytes of one of
// them pass the strict nonce check every time it is submitted, and a copy of it
// taken out of a block re-applies the flag state its signer chose at that nonce
// (toggling register and deregister, and re-running the sender's engagement
// counters) without the signer. No fee is charged, and the official command line
// sets no expiry.
//
// From validatorFlagNonceFromHeight (zero = off, the live value) those two
// branches persist nonce+1, also when the flag is already in the requested state.
// Before the height, and with the setting at zero, they behave exactly as they did,
// because history that is replayed by a fresh node holds heartbeats signed at the
// same nonce as the registration that precedes them.

// SetValidatorFlagNonceActivationHeight sets the first block height from which a
// zero-value validator register or deregister consumes the sender's nonce. A zero
// height leaves both as they were. A chain rule: the same value on every validator,
// chosen above the chain tip, all restarted together.
func (sp *StateProcessor) SetValidatorFlagNonceActivationHeight(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.validatorFlagNonceFromHeight = fromHeight
}

// validatorFlagNonceActive reports whether the block being executed is at or past
// the activation height.
func (sp *StateProcessor) validatorFlagNonceActive() bool {
	if sp == nil || sp.validatorFlagNonceFromHeight == 0 {
		return false
	}
	return sp.blockHeight() >= sp.validatorFlagNonceFromHeight
}

// consumeValidatorFlagNonce persists nonce+1 for the sender of a validator register
// or deregister that carries no value, from the activation height; before it, and
// with the setting at zero, it does nothing. account is the sender's account as it
// was read for the transaction (its nonce is the transaction's nonce, which the
// caller has already checked); the account is read again from state, so the flag
// change made just before this call is kept.
func (sp *StateProcessor) consumeValidatorFlagNonce(sender []byte, account *types.Account) error {
	if !sp.validatorFlagNonceActive() {
		return nil
	}
	return sp.updateSenderNonce(sender, nil, account.Nonce+1)
}

// SetValidatorFlagNonceActivationHeight sets the height on the node and on its state
// processor (see StateProcessor.SetValidatorFlagNonceActivationHeight). A chain
// rule: the same value on every validator, set before the chain reaches the height.
func (n *Node) SetValidatorFlagNonceActivationHeight(height uint64) {
	if n == nil {
		return
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	n.validatorFlagNonceHeight = height
	if n.state != nil {
		n.state.SetValidatorFlagNonceActivationHeight(height)
	}
}
