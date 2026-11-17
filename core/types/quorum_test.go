package types

import (
	"math/big"
	"testing"
)

// legacyQuorumThreshold is the formula every quorum check used before
// QuorumThreshold existed: floor((2*total + 2) / 3). It is kept here only to
// prove which totals the strict rule changes.
func legacyQuorumThreshold(total *big.Int) *big.Int {
	threshold := new(big.Int).Mul(total, big.NewInt(2))
	threshold.Add(threshold, big.NewInt(2))
	return threshold.Div(threshold, big.NewInt(3))
}

func bigPow10(exp int64, mult int64) *big.Int {
	v := new(big.Int).Exp(big.NewInt(10), big.NewInt(exp), nil)
	return v.Mul(v, big.NewInt(mult))
}

func TestQuorumThresholdIsStrictlyMoreThanTwoThirds(t *testing.T) {
	three := big.NewInt(3)
	for total := int64(1); total <= 5000; total++ {
		totalBig := big.NewInt(total)
		threshold := QuorumThreshold(totalBig)

		want := big.NewInt(2*total/3 + 1)
		if threshold.Cmp(want) != 0 {
			t.Fatalf("total %d: threshold %s, want floor(2*total/3)+1 = %s", total, threshold, want)
		}
		// Strictly above 2/3: threshold/total > 2/3 <=> 3*threshold > 2*total.
		if new(big.Int).Mul(threshold, three).Cmp(new(big.Int).Mul(totalBig, big.NewInt(2))) <= 0 {
			t.Fatalf("total %d: threshold %s is not strictly more than two thirds", total, threshold)
		}
		// ...and the smallest such integer.
		below := new(big.Int).Sub(threshold, big.NewInt(1))
		if new(big.Int).Mul(below, three).Cmp(new(big.Int).Mul(totalBig, big.NewInt(2))) > 0 {
			t.Fatalf("total %d: %s would already be more than two thirds", total, below)
		}
		if threshold.Cmp(totalBig) > 0 {
			t.Fatalf("total %d: threshold %s exceeds the total, no quorum could ever form", total, threshold)
		}
	}
}

// TestQuorumThresholdMatchesLegacyExceptMultiplesOfThree pins the DC-01
// compatibility claim: for every total that is not divisible by 3 the strict
// threshold equals the old ceil(2T/3) one, so every historical decision on
// such a total is unchanged; for a multiple of 3 it is exactly one unit
// stricter (that is the fix).
func TestQuorumThresholdMatchesLegacyExceptMultiplesOfThree(t *testing.T) {
	three := big.NewInt(3)
	check := func(total *big.Int) {
		t.Helper()
		newT := QuorumThreshold(total)
		oldT := legacyQuorumThreshold(total)
		if new(big.Int).Mod(total, three).Sign() == 0 {
			if newT.Cmp(new(big.Int).Add(oldT, big.NewInt(1))) != 0 {
				t.Fatalf("total %s (multiple of 3): new %s, old %s, want old+1", total, newT, oldT)
			}
			return
		}
		if newT.Cmp(oldT) != 0 {
			t.Fatalf("total %s: new threshold %s differs from the legacy %s", total, newT, oldT)
		}
	}

	for total := int64(1); total <= 20000; total++ {
		check(big.NewInt(total))
	}

	// Totals that exist or have existed on the live chain: genesis (one
	// validator, power 10), one validator holding the 10,000 ZNHB minimum
	// stake, and the current two validators of 10,000 ZNHB each.
	liveTotals := []*big.Int{
		big.NewInt(10),
		bigPow10(22, 1),
		bigPow10(22, 2),
	}
	for _, total := range liveTotals {
		if new(big.Int).Mod(total, three).Sign() == 0 {
			t.Fatalf("live total %s is a multiple of 3; the identical-threshold claim would not hold", total)
		}
		check(total)
	}
	// Other stake-scale totals, every one of them either unchanged or (for
	// multiples of 3) strictly one unit stricter.
	for _, mult := range []int64{1, 2, 3, 4, 5, 6, 7, 9, 10, 12, 15, 33, 100, 999} {
		check(bigPow10(18, mult))
		check(bigPow10(22, mult))
		check(bigPow10(24, mult))
	}
}

// TestQuorumDecisionsUnchangedOnLiveValidatorSets proves, for every subset of
// signers, that a validator set with one validator, or with two validators of
// equal power (the only shapes the live chain has ever had), reaches the same
// quorum decision under the strict rule as under the legacy one, whether or
// not the total is a multiple of 3.
func TestQuorumDecisionsUnchangedOnLiveValidatorSets(t *testing.T) {
	powers := []*big.Int{
		big.NewInt(1),
		big.NewInt(3),
		big.NewInt(10),
		bigPow10(18, 1),
		bigPow10(22, 1),
		bigPow10(22, 3),
	}
	for _, p := range powers {
		sets := map[string][]*big.Int{
			"one validator":        {p},
			"two equal validators": {p, p},
		}
		for name, set := range sets {
			total := new(big.Int)
			for _, v := range set {
				total.Add(total, v)
			}
			for mask := 0; mask < 1<<len(set); mask++ {
				signed := new(big.Int)
				for i, v := range set {
					if mask&(1<<i) != 0 {
						signed.Add(signed, v)
					}
				}
				legacy := signed.Cmp(legacyQuorumThreshold(total)) >= 0
				if got := HasQuorum(signed, total); got != legacy {
					t.Fatalf("%s with power %s, signer mask %b: strict rule says %t, legacy rule says %t", name, p, mask, got, legacy)
				}
			}
		}
	}
}

func TestHasQuorumRejectsExactlyTwoThirds(t *testing.T) {
	tests := []struct {
		name   string
		signed int64
		total  int64
		want   bool
	}{
		{"3 total, 2 signed", 2, 3, false},
		{"3 total, 3 signed", 3, 3, true},
		{"6 total, 4 signed", 4, 6, false},
		{"6 total, 5 signed", 5, 6, true},
		{"9 total, 6 signed", 6, 9, false},
		{"9 total, 7 signed", 7, 9, true},
		{"4 total, 2 signed", 2, 4, false},
		{"4 total, 3 signed", 3, 4, true},
		{"5 total, 3 signed", 3, 5, false},
		{"5 total, 4 signed", 4, 5, true},
		{"1 total, 1 signed", 1, 1, true},
		{"1 total, 0 signed", 0, 1, false},
		{"2 total, 1 signed", 1, 2, false},
		{"2 total, 2 signed", 2, 2, true},
	}
	for _, tt := range tests {
		if got := HasQuorum(big.NewInt(tt.signed), big.NewInt(tt.total)); got != tt.want {
			t.Errorf("%s: HasQuorum = %t, want %t", tt.name, got, tt.want)
		}
	}
}

func TestHasQuorumDegenerateInputs(t *testing.T) {
	if HasQuorum(nil, big.NewInt(3)) {
		t.Errorf("nil signed power must not be a quorum")
	}
	if HasQuorum(big.NewInt(3), nil) {
		t.Errorf("nil total power must not be a quorum")
	}
	if HasQuorum(big.NewInt(0), big.NewInt(0)) {
		t.Errorf("an empty validator set must not produce a quorum")
	}
	if HasQuorum(big.NewInt(5), big.NewInt(-3)) {
		t.Errorf("a negative total must not produce a quorum")
	}
}
