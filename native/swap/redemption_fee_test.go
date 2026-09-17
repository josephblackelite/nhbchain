package swap

import (
	"math/big"
	"testing"
)

// TestComputeRedemptionFee exercises the fee formula at the default policy
// (1% rate, $1 floor, $1,000 cap) against the exact worked examples used to
// justify those defaults: the floor dominates below $100, a clean 1% applies
// between $100 and $100,000, and the cap dominates above $100,000.
func TestComputeRedemptionFee(t *testing.T) {
	floor, ok := new(big.Int).SetString(DefaultRedemptionFeeFloorWei, 10)
	if !ok {
		t.Fatalf("parse default floor")
	}
	capWei, ok := new(big.Int).SetString(DefaultRedemptionFeeCapWei, 10)
	if !ok {
		t.Fatalf("parse default cap")
	}
	params := RedemptionFeeParameters{FeeBps: DefaultRedemptionFeeBps, FeeFloorWei: floor, FeeCapWei: capWei}

	oneNHB := func(n int64) *big.Int {
		return new(big.Int).Mul(big.NewInt(n), big.NewInt(1_000_000_000_000_000_000))
	}

	cases := []struct {
		name       string
		amountWei  *big.Int
		wantFeeWei *big.Int
	}{
		{"5 NHB minimum tx -> floor dominates", oneNHB(5), oneNHB(1)},
		{"50 NHB -> floor dominates (1% would be 0.5)", oneNHB(50), oneNHB(1)},
		{"100 NHB -> exactly at floor/rate boundary", oneNHB(100), oneNHB(1)},
		{"5,000 NHB -> clean 1%", oneNHB(5_000), oneNHB(50)},
		{"100,000 NHB -> exactly at rate/cap boundary", oneNHB(100_000), oneNHB(1_000)},
		{"1,000,000 NHB -> cap dominates (1% would be 10,000)", oneNHB(1_000_000), oneNHB(1_000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeRedemptionFee(tc.amountWei, params)
			if got.Cmp(tc.wantFeeWei) != 0 {
				t.Fatalf("ComputeRedemptionFee(%s) = %s, want %s", tc.amountWei, got, tc.wantFeeWei)
			}
		})
	}
}

// TestComputeRedemptionFeeNilOrNonPositiveAmount confirms a nil, zero, or
// negative amount never produces a negative or panicking result.
func TestComputeRedemptionFeeNilOrNonPositiveAmount(t *testing.T) {
	params := RedemptionFeeParameters{FeeBps: 100, FeeFloorWei: big.NewInt(1), FeeCapWei: big.NewInt(1000)}
	for _, amount := range []*big.Int{nil, big.NewInt(0), big.NewInt(-5)} {
		if got := ComputeRedemptionFee(amount, params); got.Sign() != 0 {
			t.Fatalf("ComputeRedemptionFee(%v) = %s, want 0", amount, got)
		}
	}
}
