package rewards

import "math/big"

// Accumulator tracks per-block accruals for an epoch.
type Accumulator struct {
	Epoch           uint64
	Length          uint64
	BlocksProcessed uint64

	ValidatorsPlanned *big.Int
	StakersPlanned    *big.Int
	EngagementPlanned *big.Int

	ValidatorsAccrued *big.Int
	StakersAccrued    *big.Int
	EngagementAccrued *big.Int

	validatorBase *big.Int
	stakerBase    *big.Int
	engageBase    *big.Int

	validatorRemainder uint64
	stakerRemainder    uint64
	engageRemainder    uint64
}

// NewAccumulator initialises a fresh accumulator for the supplied epoch.
func NewAccumulator(epoch, length uint64, validators, stakers, engagement *big.Int) *Accumulator {
	acc := &Accumulator{
		Epoch:             epoch,
		Length:            length,
		ValidatorsPlanned: normalizeBig(validators),
		StakersPlanned:    normalizeBig(stakers),
		EngagementPlanned: normalizeBig(engagement),
		ValidatorsAccrued: big.NewInt(0),
		StakersAccrued:    big.NewInt(0),
		EngagementAccrued: big.NewInt(0),
		validatorBase:     big.NewInt(0),
		stakerBase:        big.NewInt(0),
		engageBase:        big.NewInt(0),
	}
	if length == 0 {
		return acc
	}
	lengthBig := big.NewInt(int64(length))
	acc.validatorBase, acc.validatorRemainder = splitPerBlock(acc.ValidatorsPlanned, lengthBig)
	acc.stakerBase, acc.stakerRemainder = splitPerBlock(acc.StakersPlanned, lengthBig)
	acc.engageBase, acc.engageRemainder = splitPerBlock(acc.EngagementPlanned, lengthBig)
	return acc
}

// AccrueBlock applies per-block accrual for all categories.
func (a *Accumulator) AccrueBlock() {
	if a == nil || a.Length == 0 {
		return
	}
	a.BlocksProcessed++
	accrueCategory(a.ValidatorsAccrued, a.validatorBase)
	accrueCategory(a.StakersAccrued, a.stakerBase)
	accrueCategory(a.EngagementAccrued, a.engageBase)

	if a.validatorRemainder > 0 && a.BlocksProcessed <= a.validatorRemainder {
		a.ValidatorsAccrued.Add(a.ValidatorsAccrued, big.NewInt(1))
	}
	if a.stakerRemainder > 0 && a.BlocksProcessed <= a.stakerRemainder {
		a.StakersAccrued.Add(a.StakersAccrued, big.NewInt(1))
	}
	if a.engageRemainder > 0 && a.BlocksProcessed <= a.engageRemainder {
		a.EngagementAccrued.Add(a.EngagementAccrued, big.NewInt(1))
	}
}

func splitPerBlock(total, length *big.Int) (*big.Int, uint64) {
	if total == nil {
		return big.NewInt(0), 0
	}
	base := big.NewInt(0)
	remainder := big.NewInt(0)
	base.QuoRem(new(big.Int).Set(total), length, remainder)
	return base, remainder.Uint64()
}

func accrueCategory(target, base *big.Int) {
	if target == nil || base == nil {
		return
	}
	target.Add(target, base)
}

func normalizeBig(value *big.Int) *big.Int {
	if value == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(value)
}
