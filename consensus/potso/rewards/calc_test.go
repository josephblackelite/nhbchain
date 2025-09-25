package rewards

import (
	"math/big"
	"testing"
)

func mustAddress(bytes ...byte) [20]byte {
	var addr [20]byte
	copy(addr[:], bytes)
	return addr
}

func TestNormalizedWeights(t *testing.T) {
	a := mustAddress(0x01)
	b := mustAddress(0x02)
	weights := []WeightEntry{
		{Address: a, Weight: big.NewInt(5)},
		{Address: b, Weight: big.NewInt(3)},
		{Address: a, Weight: big.NewInt(2)},
	}
	normalized, total, err := NormalizedWeights(weights)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if total.Cmp(big.NewInt(10)) != 0 {
		t.Fatalf("expected total 10 got %s", total.String())
	}
	if len(normalized) != 2 {
		t.Fatalf("expected 2 entries got %d", len(normalized))
	}
	if normalized[0].Address != a || normalized[0].Weight.Cmp(big.NewInt(7)) != 0 {
		t.Fatalf("unexpected entry for A: %+v", normalized[0])
	}
	if normalized[1].Address != b || normalized[1].Weight.Cmp(big.NewInt(3)) != 0 {
		t.Fatalf("unexpected entry for B: %+v", normalized[1])
	}
}

func TestSplitRewardsWithDust(t *testing.T) {
	bucket := NewRoundingBucket()
	pool := big.NewInt(10)
	a := mustAddress(0x01)
	b := mustAddress(0x02)
	weights := []WeightEntry{{Address: a, Weight: big.NewInt(1)}, {Address: b, Weight: big.NewInt(2)}}
	distribution, err := SplitRewards(pool, weights, bucket)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if distribution.TotalAssigned.Cmp(big.NewInt(9)) != 0 {
		t.Fatalf("expected assigned 9 got %s", distribution.TotalAssigned.String())
	}
	if distribution.Dust.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("expected dust 1 got %s", distribution.Dust.String())
	}
	if bucket.Balance().Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("expected bucket 1 got %s", bucket.Balance().String())
	}
	nextPool := big.NewInt(5)
	nextDistribution, err := SplitRewards(nextPool, weights, bucket)
	if err != nil {
		t.Fatalf("split next: %v", err)
	}
	if nextDistribution.TotalAssigned.Cmp(big.NewInt(6)) != 0 {
		t.Fatalf("expected assigned 6 got %s", nextDistribution.TotalAssigned.String())
	}
	if nextDistribution.Dust.Sign() != 0 {
		t.Fatalf("expected no dust got %s", nextDistribution.Dust.String())
	}
	if bucket.Balance().Sign() != 0 {
		t.Fatalf("expected bucket empty got %s", bucket.Balance().String())
	}
}

func TestEntryChecksumDeterministic(t *testing.T) {
	addr := mustAddress(0xaa)
	amount := big.NewInt(12345)
	first := EntryChecksum(7, addr, amount)
	second := EntryChecksum(7, addr, amount)
	if first != second {
		t.Fatalf("expected deterministic checksum got %s != %s", first, second)
	}
}
