package emissions

import (
	"errors"
	"fmt"
	"math/big"
)

type Caps struct {
	Global *big.Int
	Epoch  *big.Int
}

type Engine struct {
	schedule *Schedule
	caps     Caps
}

func NewEngine(schedule *Schedule, caps Caps) (*Engine, error) {
	if schedule == nil {
		return nil, errors.New("emissions: schedule required")
	}
	if caps.Global != nil && caps.Global.Sign() < 0 {
		return nil, errors.New("emissions: global cap cannot be negative")
	}
	if caps.Epoch != nil && caps.Epoch.Sign() < 0 {
		return nil, errors.New("emissions: epoch cap cannot be negative")
	}
	return &Engine{schedule: schedule, caps: Caps{Global: copyBigInt(caps.Global), Epoch: copyBigInt(caps.Epoch)}}, nil
}

func copyBigInt(v *big.Int) *big.Int {
	if v == nil {
		return nil
	}
	return new(big.Int).Set(v)
}

func (e *Engine) PoolForEpoch(epoch uint64, mintedSoFar *big.Int) (*big.Int, *big.Int, error) {
	if epoch == 0 {
		return big.NewInt(0), copyBigInt(e.caps.Global), nil
	}
	if mintedSoFar != nil && mintedSoFar.Sign() < 0 {
		return nil, nil, errors.New("emissions: minted total cannot be negative")
	}
	remaining := copyBigInt(e.caps.Global)
	if remaining != nil {
		if mintedSoFar != nil {
			if mintedSoFar.Cmp(remaining) > 0 {
				return nil, nil, fmt.Errorf("emissions: minted total %s exceeds global cap %s", mintedSoFar.String(), remaining.String())
			}
			remaining.Sub(remaining, mintedSoFar)
		}
	}
	scheduled := e.schedule.AmountForEpoch(epoch)
	pool := new(big.Int).Set(scheduled)
	if e.caps.Epoch != nil && pool.Cmp(e.caps.Epoch) > 0 {
		pool.Set(e.caps.Epoch)
	}
	if remaining != nil && pool.Cmp(remaining) > 0 {
		pool.Set(remaining)
	}
	if pool.Sign() < 0 {
		pool.SetInt64(0)
	}
	if remaining != nil {
		remaining.Sub(remaining, pool)
		if remaining.Sign() < 0 {
			remaining.SetInt64(0)
		}
	}
	return pool, remaining, nil
}
