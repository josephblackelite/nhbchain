package core

// PL-R5T-22: a paymaster's signature binds the sender, from a configured height.
//
// Until the height the sponsor signs tx.Hash(), a digest that names no sender and
// that the sender's own envelope signature is over too. Two consequences, both
// reproduced with tests (core/paymaster_sponsor_binding_test.go): one sponsor
// signature validates for every sender that signs the same fields, so one
// sponsorship is reusable and the sponsor pays each sender's fee; and the same
// signature, copied into the envelope fields, is a valid envelope signature for
// the sponsor, so the sponsor becomes the sender of a copy of the transaction.
//
// From the height the sponsor signs types.Transaction.PaymasterSponsorDigest
// (a domain tag, tx.Hash() and the sender), and the signature over tx.Hash() alone
// is refused. tx.Hash(), the sender's own signature and the transaction ids do not
// change.

// SetPaymasterSponsorBinding sets the first block height from which a sponsor's
// signature must be over the digest bound to the sender. Zero (the default)
// leaves the check as it was at every height. A chain rule: the same value on
// every validator, set before the chain reaches the height.
func (sp *StateProcessor) SetPaymasterSponsorBinding(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.paymasterSponsorBindingFromHeight = fromHeight
}

// paymasterSponsorBindingActive reports whether the block being executed (or, for
// a check made outside a block, the next one) requires the bound digest.
func (sp *StateProcessor) paymasterSponsorBindingActive() bool {
	if sp == nil || sp.paymasterSponsorBindingFromHeight == 0 {
		return false
	}
	return sp.blockHeight() >= sp.paymasterSponsorBindingFromHeight
}
