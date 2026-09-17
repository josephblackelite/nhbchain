package rewards

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"sort"
	"sync"
)

// WeightEntry represents the raw composite weight for a participant when
// calculating epoch rewards.
type WeightEntry struct {
	Address [20]byte
	Weight  *big.Int
}

// NormalizedWeights normalises the supplied weight entries by merging
// duplicates and returning a deterministically ordered slice alongside the
// aggregate weight sum. Zero or nil weights are skipped.
func NormalizedWeights(weights []WeightEntry) ([]WeightEntry, *big.Int, error) {
	merged := make(map[[20]byte]*big.Int)
	total := big.NewInt(0)
	for _, entry := range weights {
		if entry.Weight == nil {
			continue
		}
		if entry.Weight.Sign() < 0 {
			return nil, nil, errors.New("rewards: weight cannot be negative")
		}
		if entry.Weight.Sign() == 0 {
			continue
		}
		acc, ok := merged[entry.Address]
		if !ok {
			acc = big.NewInt(0)
			merged[entry.Address] = acc
		}
		acc.Add(acc, entry.Weight)
	}
	normalized := make([]WeightEntry, 0, len(merged))
	for addr, weight := range merged {
		normalized = append(normalized, WeightEntry{Address: addr, Weight: new(big.Int).Set(weight)})
		total.Add(total, weight)
	}
	sort.Slice(normalized, func(i, j int) bool {
		return bytesCompare(normalized[i].Address[:], normalized[j].Address[:]) < 0
	})
	return normalized, total, nil
}

// RewardShare represents a deterministic reward allocation for a participant.
type RewardShare struct {
	Address [20]byte
	Amount  *big.Int
}

// RewardDistribution summarises an epoch reward calculation.
type RewardDistribution struct {
	Shares        []RewardShare
	TotalAssigned *big.Int
	Dust          *big.Int
}

// SplitRewards deterministically allocates the epoch pool across the supplied
// weight entries. The rounding bucket balance is applied to the pool before
// splitting and any leftover dust is returned to the bucket for the next epoch.
func SplitRewards(pool *big.Int, weights []WeightEntry, bucket *RoundingBucket) (*RewardDistribution, error) {
	if pool == nil {
		pool = big.NewInt(0)
	}
	if pool.Sign() < 0 {
		return nil, errors.New("rewards: pool cannot be negative")
	}
	normalized, totalWeight, err := NormalizedWeights(weights)
	if err != nil {
		return nil, err
	}
	effectivePool := new(big.Int).Set(pool)
	if bucket != nil {
		effectivePool = bucket.Apply(effectivePool)
	}
	distribution := &RewardDistribution{
		Shares:        make([]RewardShare, len(normalized)),
		TotalAssigned: big.NewInt(0),
		Dust:          big.NewInt(0),
	}
	if totalWeight.Sign() == 0 {
		if bucket != nil && effectivePool.Sign() > 0 {
			bucket.AddDust(effectivePool)
		}
		distribution.Dust = new(big.Int).Set(effectivePool)
		return distribution, nil
	}
	for i, entry := range normalized {
		numerator := new(big.Int).Mul(effectivePool, entry.Weight)
		quotient, _ := new(big.Int).DivMod(numerator, totalWeight, new(big.Int))
		distribution.Shares[i] = RewardShare{Address: entry.Address, Amount: quotient}
		distribution.TotalAssigned.Add(distribution.TotalAssigned, quotient)
	}
	distribution.Dust.Sub(effectivePool, distribution.TotalAssigned)
	if distribution.Dust.Sign() < 0 {
		distribution.Dust.SetInt64(0)
	}
	if bucket != nil && distribution.Dust.Sign() > 0 {
		bucket.AddDust(distribution.Dust)
	}
	return distribution, nil
}

// RoundingBucket carries forward rounding dust between epochs to ensure the
// long-term distribution exactly matches the theoretical totals.
type RoundingBucket struct {
	mu    sync.Mutex
	carry *big.Int
}

// NewRoundingBucket constructs a bucket with zero balance.
func NewRoundingBucket() *RoundingBucket {
	return &RoundingBucket{carry: big.NewInt(0)}
}

// Apply adds the current bucket balance to the pool and resets the balance.
func (b *RoundingBucket) Apply(pool *big.Int) *big.Int {
	if b == nil {
		if pool == nil {
			return big.NewInt(0)
		}
		return new(big.Int).Set(pool)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	result := big.NewInt(0)
	if pool != nil {
		result.Set(pool)
	}
	if b.carry != nil && b.carry.Sign() > 0 {
		result.Add(result, b.carry)
		b.carry.SetInt64(0)
	}
	return result
}

// AddDust accumulates leftover dust for future allocations.
func (b *RoundingBucket) AddDust(dust *big.Int) {
	if b == nil || dust == nil || dust.Sign() <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.carry == nil {
		b.carry = big.NewInt(0)
	}
	b.carry.Add(b.carry, dust)
}

// Balance returns the current bucket balance.
func (b *RoundingBucket) Balance() *big.Int {
	if b == nil {
		return big.NewInt(0)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.carry == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(b.carry)
}

// EntryChecksum derives a deterministic checksum for a ledger entry based on
// epoch, address and amount to provide a stable idempotency key.
func EntryChecksum(epoch uint64, addr [20]byte, amount *big.Int) string {
	if amount == nil {
		amount = big.NewInt(0)
	}
	payload := make([]byte, 0, 8+len(addr)+len(amount.String()))
	epochBytes := make([]byte, 8)
	for i := uint(0); i < 8; i++ {
		epochBytes[7-i] = byte(epoch >> (i * 8))
	}
	payload = append(payload, epochBytes...)
	payload = append(payload, addr[:]...)
	payload = append(payload, []byte(amount.String())...)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func bytesCompare(a, b []byte) int {
	if len(a) != len(b) {
		min := len(a)
		if len(b) < min {
			min = len(b)
		}
		for i := 0; i < min; i++ {
			if a[i] != b[i] {
				return int(a[i]) - int(b[i])
			}
		}
		return len(a) - len(b)
	}
	for i := range a {
		if a[i] != b[i] {
			return int(a[i]) - int(b[i])
		}
	}
	return 0
}
