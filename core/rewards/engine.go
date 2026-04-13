package rewards

import (
	"math/big"
	"time"
)

const (
	secondsPerYear   = 365 * 24 * 60 * 60
	basisPointsDenom = 10_000
	indexScale       = int64(1_000_000_000_000_000_000)
)

var (
	indexScaleBig = big.NewInt(indexScale)
	accrualDenom  = big.NewInt(secondsPerYear * basisPointsDenom)
)

// Engine tracks the global staking reward index and last update timestamp.
type Engine struct {
	index      *big.Int
	lastUpdate uint64
}

// NewEngine constructs a reward engine with the index initialised to one.
func NewEngine() *Engine {
	return &Engine{index: new(big.Int).Set(indexScaleBig), lastUpdate: 0}
}

// Clone returns a deep copy of the engine state.
func (e *Engine) Clone() *Engine {
	if e == nil {
		return NewEngine()
	}
	clone := &Engine{lastUpdate: e.lastUpdate}
	clone.index = new(big.Int).Set(e.currentIndex())
	return clone
}

// Index returns the current global index value.
func (e *Engine) Index() *big.Int {
	if e == nil {
		return new(big.Int).Set(indexScaleBig)
	}
	return new(big.Int).Set(e.currentIndex())
}

// SetIndex overrides the internal index value.
func (e *Engine) SetIndex(value *big.Int) {
	if e == nil {
		return
	}
	if value == nil || value.Sign() <= 0 {
		e.index = new(big.Int).Set(indexScaleBig)
		return
	}
	e.index = new(big.Int).Set(value)
}

// LastUpdateTs exposes the timestamp of the most recent accrual event.
func (e *Engine) LastUpdateTs() uint64 {
	if e == nil {
		return 0
	}
	return e.lastUpdate
}

// SetLastUpdateTs updates the stored last update timestamp.
func (e *Engine) SetLastUpdateTs(ts uint64) {
	if e == nil {
		return
	}
	e.lastUpdate = ts
}

// UpdateGlobalIndex advances the index based on the elapsed time since the last update.
// The APR is expressed in basis points and applied as simple (non-compounding) interest.
// The returned index is a copy of the internal value, and the boolean flag indicates
// whether the engine state changed (either the index or last update timestamp).
func (e *Engine) UpdateGlobalIndex(blockTs time.Time, aprBps uint64) (*big.Int, bool) {
	if e == nil {
		return new(big.Int).Set(indexScaleBig), false
	}
	e.ensureIndex()

	ts := uint64(blockTs.UTC().Unix())
	changed := false

	if e.lastUpdate == 0 {
		if ts != 0 {
			e.lastUpdate = ts
			changed = true
		}
		return new(big.Int).Set(e.index), changed
	}

	if ts <= e.lastUpdate {
		if ts != e.lastUpdate {
			e.lastUpdate = ts
			changed = true
		}
		return new(big.Int).Set(e.index), changed
	}

	delta := ts - e.lastUpdate
	e.lastUpdate = ts
	changed = true
	if aprBps == 0 {
		return new(big.Int).Set(e.index), changed
	}

	deltaBig := new(big.Int).SetUint64(delta)
	aprBig := new(big.Int).SetUint64(uint64(aprBps))
	increment := new(big.Int).Mul(deltaBig, aprBig)
	increment.Mul(increment, indexScaleBig)
	increment.Quo(increment, accrualDenom)

	if increment.Sign() > 0 {
		e.index.Add(e.index, increment)
	}
	return new(big.Int).Set(e.index), true
}

// IndexUnit returns the scaling factor applied to the staking index.
func IndexUnit() *big.Int {
	return new(big.Int).Set(indexScaleBig)
}

func (e *Engine) ensureIndex() {
	if e.index == nil || e.index.Sign() <= 0 {
		e.index = new(big.Int).Set(indexScaleBig)
	}
}

func (e *Engine) currentIndex() *big.Int {
	if e.index == nil {
		return indexScaleBig
	}
	return e.index
}
