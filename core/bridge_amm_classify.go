package core

import "errors"

// classifyBridgeAmmError gives the bridge's and the pool's refusals their disposition (ledger PL-BC-C, PL-BC-B).
// Every error a transaction of these types can return is listed, so none of them falls to the default
// quarantine: a refusal that is a pure function of the transaction's own bytes and committed rules is pruned, one
// that a later block can undo (a balance, the escrow, the pool's reserves, the swap opening, a pause, a window, a
// waiting release not yet due) is skipped.
func classifyBridgeAmmError(err error) (proposalTxDisposition, bool) {
	switch {
	case errors.Is(err, ErrBridgeInvalidPayload),
		errors.Is(err, ErrBridgeAdminWallet),
		errors.Is(err, ErrBridgeAboveMax),
		errors.Is(err, ErrBridgeAboveMaxRelease),
		errors.Is(err, ErrBridgeNotGuardian),
		errors.Is(err, ErrBridgeBadSignatures),
		errors.Is(err, ErrBridgeAlreadyReleased),
		errors.Is(err, ErrAmmInvalidPayload),
		errors.Is(err, ErrAmmUnauthorized),
		errors.Is(err, ErrAmmAdminWallet):
		return proposalDispositionPrune, true
	case errors.Is(err, ErrBridgeInsufficientBalance),
		errors.Is(err, ErrBridgeEscrowShort),
		errors.Is(err, ErrBridgePaused),
		errors.Is(err, ErrBridgeWindowCap),
		errors.Is(err, ErrBridgeNotPending),
		errors.Is(err, ErrBridgeNotYet),
		errors.Is(err, ErrAmmSwapsClosed),
		errors.Is(err, ErrAmmInsufficientBalance),
		errors.Is(err, ErrAmmSlippage),
		errors.Is(err, ErrAmmTooLarge),
		errors.Is(err, ErrAmmReserveFloor),
		errors.Is(err, ErrAmmEmptyPool),
		errors.Is(err, ErrAmmZeroOutput):
		return proposalDispositionSkip, true
	}
	return 0, false
}
