package rewards

import "math/big"

const (
	// HalvingBaseEmissionZNHB is B0: the whole-ZNHB emission per epoch
	// during the first halving era, before any halving has occurred.
	HalvingBaseEmissionZNHB = 50_000

	// HalvingEraLengthEpochs is E: the number of epochs in each halving
	// era. Chosen so a full era at the current era's rate emits exactly
	// half of whatever total the Reward Pool had remaining before that
	// era began -- the same Bitcoin-style property that guarantees the
	// infinite sum of every era's emission converges to a fixed ceiling
	// (2 * HalvingBaseEmissionZNHB * HalvingEraLengthEpochs = 200,000,000
	// ZNHB at these values) and never exceeds it.
	HalvingEraLengthEpochs = 2_000

	// maxHalvingEras bounds how many EmissionStep entries
	// HalvingSchedule builds. Past this many halvings the per-epoch
	// emission is already zero at attoZNHB precision (50,000 ZNHB is
	// ~2^75.4 attoZNHB, so era 76 onward always rounds to zero) --
	// further steps would be dead weight.
	maxHalvingEras = 80
)

var weiPerWholeZNHB = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

// HalvingEmissionForEpoch returns the base validator/staking reward
// emission for the given 1-indexed epoch under the halving schedule: B0
// ZNHB/epoch for the first HalvingEraLengthEpochs epochs, half that for the
// next era, and so on, halving every era. Integer halving is applied at
// attoZNHB (wei) precision via a right shift -- this rounds every era's
// emission DOWN, so the schedule's cumulative total always converges to
// strictly less than 2*B0*E ZNHB, never reaching or exceeding the fixed
// Reward Pool that is meant to back it.
func HalvingEmissionForEpoch(epoch uint64) *big.Int {
	if epoch == 0 {
		return big.NewInt(0)
	}
	era := (epoch - 1) / HalvingEraLengthEpochs
	if era >= maxHalvingEras {
		return big.NewInt(0)
	}
	base := new(big.Int).Mul(big.NewInt(HalvingBaseEmissionZNHB), weiPerWholeZNHB)
	return base.Rsh(base, uint(era))
}

// HalvingSchedule materializes HalvingEmissionForEpoch as an explicit,
// finite []EmissionStep -- one entry per halving era, stopping once the
// emission has rounded to zero -- so it can be assigned directly to
// Config.Schedule and driven entirely by the existing, already-tested
// EmissionForEpoch step-lookup mechanism instead of a parallel code path.
func HalvingSchedule() []EmissionStep {
	steps := make([]EmissionStep, 0, maxHalvingEras)
	for era := uint64(0); era < maxHalvingEras; era++ {
		startEpoch := era*HalvingEraLengthEpochs + 1
		amount := HalvingEmissionForEpoch(startEpoch)
		if amount.Sign() == 0 {
			break
		}
		steps = append(steps, EmissionStep{StartEpoch: startEpoch, Amount: amount})
	}
	return steps
}

// HalvingScheduleConfig builds a Config driven by the halving schedule
// above. validatorSplitBps, stakerSplitBps, and engagementSplitBps must sum
// to SplitDenominator (10,000), matching Config.Validate's existing rule.
// This is a constructor, not an activation switch -- nothing calls it
// automatically. A caller (test, ops tooling, or a future genesis/governance
// path) still has to pass the result to StateProcessor.SetRewardConfig to
// actually start validator/staking rewards flowing.
func HalvingScheduleConfig(validatorSplitBps, stakerSplitBps, engagementSplitBps uint32, historyLength uint64) Config {
	return Config{
		Schedule:        HalvingSchedule(),
		ValidatorSplit:  validatorSplitBps,
		StakerSplit:     stakerSplitBps,
		EngagementSplit: engagementSplitBps,
		HistoryLength:   historyLength,
	}
}
