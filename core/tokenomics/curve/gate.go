package curve

// ReleaseGate reports whether a given tranche is currently unlockable for
// sale out of the treasury. The genesis default (AlwaysOpenGate) makes
// every tranche purchasable immediately. Future progressive treasury
// release -- gating not-yet-sold tranches behind time, usage, or a
// governance vote (tokenomics design doc, section 7) -- implements this
// same interface without any change to curve.go, applyBuyZNHB, or
// applySwapVoucherMintTransaction.
type ReleaseGate interface {
	IsPurchasable(trancheIndex uint64, state GateState) (bool, error)
}

// GateState carries whatever on-chain context a gate implementation needs
// (block height, timestamp, future governance-set flags). Populated by the
// caller from consensus state it already has -- this package intentionally
// has no dependency on core/state, to avoid an import cycle.
type GateState struct {
	Height    uint64
	Timestamp int64
}

// AlwaysOpenGate is the genesis default: every tranche is purchasable
// immediately, no gating at all.
type AlwaysOpenGate struct{}

// IsPurchasable always returns true.
func (AlwaysOpenGate) IsPurchasable(uint64, GateState) (bool, error) {
	return true, nil
}
