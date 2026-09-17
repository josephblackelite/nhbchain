package potso

import (
	"math/big"
	"testing"
)

func TestComputeWeightSnapshotClampAndDecay(t *testing.T) {
	params := WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           10,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 100,
		MinStakeToWinWei:      big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   1,
		TopKWinners:           0,
		TieBreak:              TieBreakAddrLex,
	}
	inputs := []WeightInput{
		{
			Address:            addr(1),
			Stake:              big.NewInt(100),
			PreviousEngagement: 80,
			Meter:              EngagementMeter{TxCount: 40},
		},
		{
			Address:            addr(2),
			Stake:              big.NewInt(0),
			PreviousEngagement: 0,
			Meter:              EngagementMeter{TxCount: 2},
		},
	}
	snapshot, err := ComputeWeightSnapshot(10, inputs, params)
	if err != nil {
		t.Fatalf("compute snapshot: %v", err)
	}
	if snapshot.TotalEngagement != 100+10 {
		t.Fatalf("expected engagement total 110, got %d", snapshot.TotalEngagement)
	}
	if snapshot.Entries[0].Engagement != 100 {
		t.Fatalf("expected first entry engagement capped at 100, got %d", snapshot.Entries[0].Engagement)
	}
	if snapshot.Entries[1].Engagement != 10 {
		t.Fatalf("expected second entry engagement 10, got %d", snapshot.Entries[1].Engagement)
	}
}

func TestComputeWeightSnapshotFilters(t *testing.T) {
	params := WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           10,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 1000,
		MinStakeToWinWei:      big.NewInt(50),
		MinEngagementToWin:    20,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           0,
	}
	inputs := []WeightInput{
		{
			Address:            addr(3),
			Stake:              big.NewInt(40),
			PreviousEngagement: 0,
			Meter:              EngagementMeter{TxCount: 10},
		},
		{
			Address:            addr(4),
			Stake:              big.NewInt(60),
			PreviousEngagement: 0,
			Meter:              EngagementMeter{TxCount: 1},
		},
	}
	snapshot, err := ComputeWeightSnapshot(1, inputs, params)
	if err != nil {
		t.Fatalf("compute snapshot: %v", err)
	}
	if len(snapshot.Entries) != 0 {
		t.Fatalf("expected all entries filtered, got %d", len(snapshot.Entries))
	}
	if snapshot.TotalStake.Sign() != 0 {
		t.Fatalf("expected zero total stake, got %s", snapshot.TotalStake)
	}
}

// TestComputeBetaScaledHalfLifeOne locks in the halfLife=1 edge case. This
// value is also implicitly exercised by TestComputeWeightSnapshotClampAndDecay,
// but is asserted directly here against the exact value the previous
// math.Pow(2, -1)/math.Round formula produced (500_000_000 = engagementBetaScale/2),
// computed once offline as a fixed reference constant.
func TestComputeBetaScaledHalfLifeOne(t *testing.T) {
	const wantOldFloatFormula = 500_000_000
	if got := computeBetaScaled(1); got != wantOldFloatFormula {
		t.Fatalf("computeBetaScaled(1) = %d, want %d", got, wantOldFloatFormula)
	}
}

// TestComputeBetaScaledZeroHalfLife locks in the halfLife=0 guard clause,
// which returns 0 without entering the fixed-point search.
func TestComputeBetaScaledZeroHalfLife(t *testing.T) {
	if got := computeBetaScaled(0); got != 0 {
		t.Fatalf("computeBetaScaled(0) = %d, want 0", got)
	}
}

// TestComputeBetaScaledLargeHalfLife exercises a halfLife far larger than any
// value configured in production (config.toml/prod.toml use 7) or any other
// existing test, and cross-checks the new big.Int binary-search
// implementation against a fixed reference constant computed once offline
// from the OLD math.Pow(2, -1/halfLife)*math.Round formula (not by calling
// math.Pow at test time), proving the two methods agree at this scale.
func TestComputeBetaScaledLargeHalfLife(t *testing.T) {
	const halfLife = 10_000
	const wantOldFloatFormula = 999_930_688
	if got := computeBetaScaled(halfLife); got != wantOldFloatFormula {
		t.Fatalf("computeBetaScaled(%d) = %d, want %d", halfLife, got, wantOldFloatFormula)
	}
}

// TestIntegerNthRootRoundedExcessZero covers the excess=0 edge case used by
// computeComposite's quadratic dampening branch's defensive guard.
func TestIntegerNthRootRoundedExcessZero(t *testing.T) {
	if got := integerNthRootRounded(0, 2); got != 0 {
		t.Fatalf("integerNthRootRounded(0, 2) = %d, want 0", got)
	}
}

// TestIntegerNthRootRoundedNonEvenPower covers a QuadraticTxDampenPower that
// does not evenly divide the excess (1234 is not a perfect cube: 10^3=1000,
// 11^3=1331), exercising the round-half-away-from-zero comparison rather than
// an exact root. The expected value was computed once offline from the OLD
// math.Pow(excess, 1/power)/math.Round formula as a fixed reference constant.
func TestIntegerNthRootRoundedNonEvenPower(t *testing.T) {
	const excess, power = 1234, 3
	const wantOldFloatFormula = 11
	if got := integerNthRootRounded(excess, power); got != wantOldFloatFormula {
		t.Fatalf("integerNthRootRounded(%d, %d) = %d, want %d", excess, power, got, wantOldFloatFormula)
	}
}

// TestIntegerNthRootRoundedLargeValueCrossCheck cross-checks the new big.Int
// binary-search implementation against the OLD math.Pow/math.Round formula at
// a magnitude (2^62) far beyond anything reachable by realistic tx counts,
// using a fixed reference constant computed once offline (not by calling
// math.Pow at test time).
func TestIntegerNthRootRoundedLargeValueCrossCheck(t *testing.T) {
	const excess, power = 1 << 62, 2
	const wantOldFloatFormula = 1 << 31 // 2147483648
	if got := integerNthRootRounded(excess, power); got != wantOldFloatFormula {
		t.Fatalf("integerNthRootRounded(%d, %d) = %d, want %d", excess, power, got, wantOldFloatFormula)
	}
}

func TestComputeWeightSnapshotTopK(t *testing.T) {
	params := WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           10,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 1000,
		MinStakeToWinWei:      big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           2,
		TieBreak:              TieBreakAddrLex,
	}
	inputs := []WeightInput{
		{Address: addr(5), Stake: big.NewInt(10), Meter: EngagementMeter{TxCount: 1}},
		{Address: addr(6), Stake: big.NewInt(20), Meter: EngagementMeter{TxCount: 2}},
		{Address: addr(7), Stake: big.NewInt(30), Meter: EngagementMeter{TxCount: 3}},
	}
	snapshot, err := ComputeWeightSnapshot(2, inputs, params)
	if err != nil {
		t.Fatalf("compute snapshot: %v", err)
	}
	if len(snapshot.Entries) != 2 {
		t.Fatalf("expected 2 entries after top-k, got %d", len(snapshot.Entries))
	}
	if snapshot.Entries[0].Address != addr(7) || snapshot.Entries[1].Address != addr(6) {
		t.Fatalf("unexpected ordering after top-k")
	}
}

func TestWeightParamsValidateRejectsUnsafeDecayHalfLife(t *testing.T) {
	params := DefaultWeightParams()
	params.DecayHalfLifeEpochs = maxDecayHalfLifeEpochs + 1
	if err := params.Validate(); err == nil {
		t.Fatalf("expected validation error for DecayHalfLifeEpochs exceeding the safe bound")
	}

	params.DecayHalfLifeEpochs = maxDecayHalfLifeEpochs
	if err := params.Validate(); err != nil {
		t.Fatalf("expected DecayHalfLifeEpochs at the bound to validate cleanly, got %v", err)
	}
}

func TestWeightParamsValidateRejectsUnsafeQuadraticTxDampenPower(t *testing.T) {
	params := DefaultWeightParams()
	params.QuadraticTxDampenPower = maxQuadraticTxDampenPower + 1
	if err := params.Validate(); err == nil {
		t.Fatalf("expected validation error for QuadraticTxDampenPower exceeding the safe bound")
	}

	params.QuadraticTxDampenPower = maxQuadraticTxDampenPower
	if err := params.Validate(); err != nil {
		t.Fatalf("expected QuadraticTxDampenPower at the bound to validate cleanly, got %v", err)
	}
}
