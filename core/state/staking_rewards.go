package state

import (
	"errors"
	"math/big"
	"time"
)

// RewardEngine manages staking reward state transitions backed by the state manager.
type RewardEngine struct {
	mgr *Manager
}

// ErrNotReady is returned when a reward operation cannot be completed yet.
var ErrNotReady = errors.New("reward engine not ready")

// NewRewardEngine constructs a RewardEngine bound to the provided state manager.
func NewRewardEngine(mgr *Manager) *RewardEngine {
	return &RewardEngine{mgr: mgr}
}

// updateGlobalIndex advances the global reward index snapshot.
func (e *RewardEngine) updateGlobalIndex(aprBps, payoutDays uint64, now time.Time) {
	// TODO: implement global index update logic.
}

// accrue processes pending rewards for the provided account address.
func (e *RewardEngine) accrue(addr []byte) error {
	// TODO: implement account accrual logic.
	return nil
}

// settleOnDelegate records a delegation event for the given account address.
func (e *RewardEngine) settleOnDelegate(addr []byte, amount *big.Int) error {
	// TODO: implement delegation settlement logic.
	return nil
}

// settleOnUndelegate records an undelegation event for the given account address.
func (e *RewardEngine) settleOnUndelegate(addr []byte, amount *big.Int) error {
	// TODO: implement undelegation settlement logic.
	return nil
}

// claim finalizes rewards for the specified account at the provided timestamp.
func (e *RewardEngine) claim(addr []byte, now time.Time) (*big.Int, error) {
	// TODO: implement claim processing.
	return big.NewInt(0), ErrNotReady
}

// encodeUQ128x128 encodes a big integer into a UQ128x128 fixed-point representation.
func encodeUQ128x128(value *big.Int) []byte {
	// TODO: implement encoding to UQ128x128 fixed-point representation.
	return nil
}

// decodeUQ128x128 decodes a UQ128x128 fixed-point representation into a big integer.
func decodeUQ128x128(data []byte) *big.Int {
	// TODO: implement decoding from UQ128x128 fixed-point representation.
	return nil
}
