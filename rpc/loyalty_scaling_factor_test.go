package rpc

import (
	"math/big"
	"testing"
)

func TestLoyaltyScalingFactorFormat(t *testing.T) {
	cases := []struct {
		paid, proposed int64
		want           string
	}{
		{0, 0, "1.0"},
		{5, 0, "1.0"},
		{10, 10, "1.0"},
		{12, 10, "1.0"},
		{0, 10, "0.0"},
		{5, 10, "0.5"},
		{1, 3, "0.333333"},
		{999_999_999, 1_000_000_000, "0.999999"},
		{1, 1_000_000_000, "0.000001"},
	}
	for _, c := range cases {
		if got := loyaltyScalingFactor(big.NewInt(c.paid), big.NewInt(c.proposed)); got != c.want {
			t.Errorf("paid %d of %d: %q, want %q", c.paid, c.proposed, got, c.want)
		}
	}
	if got := loyaltyScalingFactor(nil, nil); got != "1.0" {
		t.Errorf("nil totals: %q, want 1.0", got)
	}
}
