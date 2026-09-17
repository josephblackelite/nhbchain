package rewards

import (
	"math/big"
	"testing"
)

func wholeZNHB(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), weiPerWholeZNHB)
}

func TestHalvingEmissionForEpoch_FirstEra(t *testing.T) {
	for _, epoch := range []uint64{1, 2, HalvingEraLengthEpochs} {
		got := HalvingEmissionForEpoch(epoch)
		want := wholeZNHB(HalvingBaseEmissionZNHB)
		if got.Cmp(want) != 0 {
			t.Fatalf("epoch %d emission = %v, want %v (first era, no halving yet)", epoch, got, want)
		}
	}
}

func TestHalvingEmissionForEpoch_HalvesAtEraBoundary(t *testing.T) {
	firstEraLast := HalvingEmissionForEpoch(HalvingEraLengthEpochs)
	secondEraFirst := HalvingEmissionForEpoch(HalvingEraLengthEpochs + 1)
	half := new(big.Int).Rsh(firstEraLast, 1)
	if secondEraFirst.Cmp(half) != 0 {
		t.Fatalf("epoch %d emission = %v, want exactly half of era 0's %v = %v", HalvingEraLengthEpochs+1, secondEraFirst, firstEraLast, half)
	}

	thirdEraFirst := HalvingEmissionForEpoch(2*HalvingEraLengthEpochs + 1)
	quarter := new(big.Int).Rsh(firstEraLast, 2)
	if thirdEraFirst.Cmp(quarter) != 0 {
		t.Fatalf("era 2 emission = %v, want a quarter of era 0's %v = %v", thirdEraFirst, firstEraLast, quarter)
	}
}

func TestHalvingEmissionForEpoch_ZeroEpochIsZero(t *testing.T) {
	if got := HalvingEmissionForEpoch(0); got.Sign() != 0 {
		t.Fatalf("epoch 0 emission = %v, want 0 (epochs are 1-indexed)", got)
	}
}

func TestHalvingEmissionForEpoch_EventuallyReachesZero(t *testing.T) {
	epoch := uint64(maxHalvingEras+1) * HalvingEraLengthEpochs
	if got := HalvingEmissionForEpoch(epoch); got.Sign() != 0 {
		t.Fatalf("epoch %d (far past all halvings) emission = %v, want 0", epoch, got)
	}
}

// TestHalvingScheduleNeverExceedsRewardPool proves the cumulative sum of
// every era's total emission (rate * HalvingEraLengthEpochs) stays strictly
// below the 200,000,000 ZNHB Reward Pool this schedule is meant to be
// backed by (2 * HalvingBaseEmissionZNHB * HalvingEraLengthEpochs) --
// integer halving's floor rounding means the schedule converges from
// below, never reaching or crossing the ceiling.
func TestHalvingScheduleNeverExceedsRewardPool(t *testing.T) {
	ceiling := new(big.Int).Mul(wholeZNHB(HalvingBaseEmissionZNHB), big.NewInt(2*HalvingEraLengthEpochs))

	steps := HalvingSchedule()
	if len(steps) == 0 {
		t.Fatalf("expected a non-empty halving schedule")
	}

	cumulative := big.NewInt(0)
	eraLength := big.NewInt(HalvingEraLengthEpochs)
	for i, step := range steps {
		eraTotal := new(big.Int).Mul(step.Amount, eraLength)
		cumulative.Add(cumulative, eraTotal)
		if cumulative.Cmp(ceiling) > 0 {
			t.Fatalf("cumulative emission after era %d (step %+v) = %v, exceeds the %v ZNHB Reward Pool ceiling", i, step, cumulative, ceiling)
		}
	}
	if cumulative.Cmp(ceiling) >= 0 {
		t.Fatalf("cumulative emission %v reached or exceeded the ceiling %v -- floor rounding should keep it strictly below", cumulative, ceiling)
	}
}

func TestHalvingScheduleStepsAreStrictlyIncreasingStartEpochs(t *testing.T) {
	steps := HalvingSchedule()
	for i := 1; i < len(steps); i++ {
		if steps[i].StartEpoch <= steps[i-1].StartEpoch {
			t.Fatalf("step %d start epoch %d is not strictly greater than step %d's %d", i, steps[i].StartEpoch, i-1, steps[i-1].StartEpoch)
		}
		if steps[i].Amount.Cmp(steps[i-1].Amount) >= 0 {
			t.Fatalf("step %d amount %v is not strictly less than step %d's %v -- schedule must strictly decrease", i, steps[i].Amount, i-1, steps[i-1].Amount)
		}
	}
}

func TestHalvingScheduleConfig_IsValid(t *testing.T) {
	cfg := HalvingScheduleConfig(2000, 5000, 3000, 64)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("HalvingScheduleConfig produced an invalid Config: %v", err)
	}
	if !cfg.IsEnabled() {
		t.Fatalf("expected HalvingScheduleConfig to be enabled")
	}
	if got := cfg.EmissionForEpoch(1); got.Cmp(wholeZNHB(HalvingBaseEmissionZNHB)) != 0 {
		t.Fatalf("EmissionForEpoch(1) via Config = %v, want %v", got, wholeZNHB(HalvingBaseEmissionZNHB))
	}
}

func TestHalvingScheduleConfig_RejectsBadSplit(t *testing.T) {
	cfg := HalvingScheduleConfig(2000, 5000, 2999, 64)
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected Validate to reject a split that doesn't sum to %d bps", SplitDenominator)
	}
}
