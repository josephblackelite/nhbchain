package state

import (
	"errors"
	"math/big"
	"time"
)

const (
	basisPointsDenom = 10_000
	secondsPerDay    = 24 * 60 * 60
	secondsPerYear   = 365 * secondsPerDay
)

var (
	uq128Unit          = new(big.Int).Lsh(big.NewInt(1), 128)
	accrualDenominator = new(big.Int).Mul(big.NewInt(secondsPerYear), big.NewInt(basisPointsDenom))
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
	if e == nil || e.mgr == nil {
		return
	}

	snapshot, err := e.mgr.GetGlobalIndex()
	if err != nil {
		return
	}
	if snapshot == nil {
		snapshot = &GlobalIndex{}
	}

	current := decodeUQ128x128(snapshot.UQ128x128)
	ts := now.UTC().Unix()

	if ts <= 0 {
		snapshot.LastUpdateUnix = ts
		snapshot.UQ128x128 = encodeUQ128x128(current)
		_ = e.mgr.PutGlobalIndex(snapshot)
		return
	}

	if snapshot.LastUpdateUnix == 0 {
		snapshot.LastUpdateUnix = ts
		snapshot.UQ128x128 = encodeUQ128x128(current)
		_ = e.mgr.PutGlobalIndex(snapshot)
		return
	}

	delta := ts - snapshot.LastUpdateUnix
	if delta <= 0 {
		snapshot.LastUpdateUnix = ts
		snapshot.UQ128x128 = encodeUQ128x128(current)
		_ = e.mgr.PutGlobalIndex(snapshot)
		return
	}

	if payoutDays > 0 {
		maxDelta := int64(payoutDays) * secondsPerDay
		if maxDelta > 0 && delta > maxDelta {
			delta = maxDelta
		}
	}

	if aprBps > 0 && delta > 0 {
		deltaBig := new(big.Int).SetInt64(delta)
		aprBig := new(big.Int).SetUint64(aprBps)
		increment := new(big.Int).Set(current)
		increment.Mul(increment, aprBig)
		increment.Mul(increment, deltaBig)
		increment.Quo(increment, accrualDenominator)

		if increment.Sign() > 0 {
			current.Add(current, increment)
		}
	}

	snapshot.LastUpdateUnix = ts
	snapshot.UQ128x128 = encodeUQ128x128(current)
	_ = e.mgr.PutGlobalIndex(snapshot)
}

// UpdateGlobalIndex is a public wrapper around updateGlobalIndex to allow other packages
// to advance the global staking index. It delegates to the internal implementation to
// avoid exposing additional state management details.
func (e *RewardEngine) UpdateGlobalIndex(aprBps, payoutDays uint64, now time.Time) {
	e.updateGlobalIndex(aprBps, payoutDays, now)
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
	if value == nil || value.Sign() <= 0 {
		value = new(big.Int).Set(uq128Unit)
	}
	encoded := make([]byte, 32)
	value.FillBytes(encoded)
	return encoded
}

// decodeUQ128x128 decodes a UQ128x128 fixed-point representation into a big integer.
func decodeUQ128x128(data []byte) *big.Int {
	if len(data) == 0 {
		return new(big.Int).Set(uq128Unit)
	}
	return new(big.Int).SetBytes(data)
}
