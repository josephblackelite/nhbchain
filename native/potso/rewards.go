package potso

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
)

const (
	// RewardBpsDenominator defines the fixed denominator used for alpha weighting.
	RewardBpsDenominator = 10000
)

// RewardConfig controls the epoch reward distribution behaviour for POTSO participants.
type RewardConfig struct {
	EpochLengthBlocks  uint64
	AlphaStakeBps      uint64
	MinPayoutWei       *big.Int
	EmissionPerEpoch   *big.Int
	TreasuryAddress    [20]byte
	MaxWinnersPerEpoch uint64
	CarryRemainder     bool
}

// RewardSnapshotEntry captures the contribution inputs for a single participant within an epoch snapshot.
type RewardSnapshotEntry struct {
	Address    [20]byte
	Stake      *big.Int
	Engagement *big.Int
}

// RewardSnapshot aggregates the stake and engagement totals used to derive payouts for an epoch.
type RewardSnapshot struct {
	Epoch           uint64
	Day             string
	StakeTotal      *big.Int
	EngagementTotal *big.Int
	Entries         []RewardSnapshotEntry
}

// RewardPayout captures the computed payout for a participant.
type RewardPayout struct {
	Address [20]byte
	Amount  *big.Int
	Weight  *big.Rat
}

// RewardOutcome summarises the distribution for a single epoch.
type RewardOutcome struct {
	Epoch     uint64
	Budget    *big.Int
	TotalPaid *big.Int
	Remainder *big.Int
	Winners   []RewardPayout
}

// RewardEpochMeta stores the persisted metadata for a processed epoch distribution.
type RewardEpochMeta struct {
	Epoch           uint64
	Day             string
	StakeTotal      *big.Int
	EngagementTotal *big.Int
	AlphaBps        uint64
	Emission        *big.Int
	Budget          *big.Int
	TotalPaid       *big.Int
	Remainder       *big.Int
	Winners         uint64
}

// Clone produces a deep copy of the metadata to protect internal references.
func (m *RewardEpochMeta) Clone() RewardEpochMeta {
	if m == nil {
		return RewardEpochMeta{}
	}
	clone := RewardEpochMeta{
		Epoch:    m.Epoch,
		Day:      m.Day,
		AlphaBps: m.AlphaBps,
		Winners:  m.Winners,
	}
	clone.StakeTotal = copyBigInt(m.StakeTotal)
	clone.EngagementTotal = copyBigInt(m.EngagementTotal)
	clone.Emission = copyBigInt(m.Emission)
	clone.Budget = copyBigInt(m.Budget)
	clone.TotalPaid = copyBigInt(m.TotalPaid)
	clone.Remainder = copyBigInt(m.Remainder)
	return clone
}

// DefaultRewardConfig returns a disabled configuration.
func DefaultRewardConfig() RewardConfig {
	return RewardConfig{
		EpochLengthBlocks:  0,
		AlphaStakeBps:      0,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(0),
		TreasuryAddress:    [20]byte{},
		MaxWinnersPerEpoch: 0,
		CarryRemainder:     true,
	}
}

// Validate ensures the configuration values fall within acceptable bounds.
func (c RewardConfig) Validate() error {
	if c.AlphaStakeBps > RewardBpsDenominator {
		return fmt.Errorf("alpha stake weight must be <= %d", RewardBpsDenominator)
	}
	if c.MinPayoutWei != nil && c.MinPayoutWei.Sign() < 0 {
		return errors.New("min payout cannot be negative")
	}
	if c.EmissionPerEpoch != nil && c.EmissionPerEpoch.Sign() < 0 {
		return errors.New("emission per epoch cannot be negative")
	}
	if c.Enabled() {
		if c.EpochLengthBlocks == 0 {
			return errors.New("epoch length must be positive when rewards are enabled")
		}
		if c.TreasuryAddress == ([20]byte{}) {
			return errors.New("treasury address must be configured when rewards are enabled")
		}
	}
	return nil
}

// Enabled reports whether the configuration results in active distributions.
func (c RewardConfig) Enabled() bool {
	return c.EpochLengthBlocks > 0 && (c.EmissionPerEpoch != nil && c.EmissionPerEpoch.Sign() > 0)
}

// ComputeRewards derives the payouts for a snapshot given the configured budget.
func ComputeRewards(cfg RewardConfig, snapshot RewardSnapshot, budget *big.Int) (*RewardOutcome, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if budget == nil {
		budget = big.NewInt(0)
	}
	if budget.Sign() <= 0 {
		return &RewardOutcome{
			Epoch:     snapshot.Epoch,
			Budget:    new(big.Int).Set(budget),
			TotalPaid: big.NewInt(0),
			Remainder: new(big.Int).Set(budget),
			Winners:   []RewardPayout{},
		}, nil
	}

	entries := normaliseEntries(snapshot.Entries)
	if len(entries) == 0 {
		return &RewardOutcome{
			Epoch:     snapshot.Epoch,
			Budget:    new(big.Int).Set(budget),
			TotalPaid: big.NewInt(0),
			Remainder: new(big.Int).Set(budget),
			Winners:   []RewardPayout{},
		}, nil
	}

	totalStake := snapshot.StakeTotal
	if totalStake == nil {
		totalStake = big.NewInt(0)
		for _, entry := range entries {
			totalStake.Add(totalStake, entry.Stake)
		}
	}
	totalEngagement := snapshot.EngagementTotal
	if totalEngagement == nil {
		totalEngagement = big.NewInt(0)
		for _, entry := range entries {
			totalEngagement.Add(totalEngagement, entry.Engagement)
		}
	}
	stakeWeight := new(big.Rat).SetFrac(big.NewInt(int64(cfg.AlphaStakeBps)), big.NewInt(RewardBpsDenominator))
	engagementWeight := new(big.Rat).Sub(big.NewRat(1, 1), stakeWeight)

	weighted := make([]RewardPayout, 0, len(entries))
	for _, entry := range entries {
		weight := computeWeight(entry, totalStake, totalEngagement, stakeWeight, engagementWeight)
		if weight.Sign() <= 0 {
			continue
		}
		weighted = append(weighted, RewardPayout{Address: entry.Address, Amount: big.NewInt(0), Weight: weight})
	}
	if len(weighted) == 0 {
		return &RewardOutcome{
			Epoch:     snapshot.Epoch,
			Budget:    new(big.Int).Set(budget),
			TotalPaid: big.NewInt(0),
			Remainder: new(big.Int).Set(budget),
			Winners:   []RewardPayout{},
		}, nil
	}

	sort.Slice(weighted, func(i, j int) bool {
		cmp := weighted[i].Weight.Cmp(weighted[j].Weight)
		if cmp == 0 {
			return lessAddress(weighted[i].Address, weighted[j].Address)
		}
		return cmp > 0
	})

	if cfg.MaxWinnersPerEpoch > 0 && uint64(len(weighted)) > cfg.MaxWinnersPerEpoch {
		weighted = weighted[:cfg.MaxWinnersPerEpoch]
	}

	totalPaid := big.NewInt(0)
	winners := make([]RewardPayout, 0, len(weighted))
	minPayout := cfg.MinPayoutWei
	if minPayout == nil {
		minPayout = big.NewInt(0)
	}
	for _, candidate := range weighted {
		payout := ratMulInt(candidate.Weight, budget)
		if payout.Sign() <= 0 {
			continue
		}
		if minPayout.Sign() > 0 && payout.Cmp(minPayout) < 0 {
			continue
		}
		winners = append(winners, RewardPayout{
			Address: candidate.Address,
			Amount:  payout,
			Weight:  candidate.Weight,
		})
		totalPaid.Add(totalPaid, payout)
	}

	remainder := new(big.Int).Sub(budget, totalPaid)
	if remainder.Sign() < 0 {
		remainder = big.NewInt(0)
	}

	return &RewardOutcome{
		Epoch:     snapshot.Epoch,
		Budget:    new(big.Int).Set(budget),
		TotalPaid: totalPaid,
		Remainder: remainder,
		Winners:   winners,
	}, nil
}

func normaliseEntries(entries []RewardSnapshotEntry) []RewardSnapshotEntry {
	if len(entries) == 0 {
		return []RewardSnapshotEntry{}
	}
	normalised := make([]RewardSnapshotEntry, len(entries))
	for i := range entries {
		entry := entries[i]
		normalised[i] = RewardSnapshotEntry{
			Address:    entry.Address,
			Stake:      copyBigInt(entry.Stake),
			Engagement: copyBigInt(entry.Engagement),
		}
	}
	return normalised
}

func copyBigInt(value *big.Int) *big.Int {
	if value == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(value)
}

func computeWeight(entry RewardSnapshotEntry, totalStake, totalEngagement *big.Int, stakeWeight, engagementWeight *big.Rat) *big.Rat {
	weight := new(big.Rat)
	if totalStake != nil && totalStake.Sign() > 0 && entry.Stake.Sign() > 0 {
		component := new(big.Rat).SetFrac(entry.Stake, totalStake)
		component.Mul(component, stakeWeight)
		weight.Add(weight, component)
	}
	if totalEngagement != nil && totalEngagement.Sign() > 0 && entry.Engagement.Sign() > 0 {
		component := new(big.Rat).SetFrac(entry.Engagement, totalEngagement)
		component.Mul(component, engagementWeight)
		weight.Add(weight, component)
	}
	return weight
}

func ratMulInt(r *big.Rat, v *big.Int) *big.Int {
	if r == nil || v == nil {
		return big.NewInt(0)
	}
	product := new(big.Rat).Mul(r, new(big.Rat).SetInt(v))
	if product.Sign() <= 0 {
		return big.NewInt(0)
	}
	quotient := new(big.Int).Div(product.Num(), product.Denom())
	if quotient.Sign() < 0 {
		quotient = big.NewInt(0)
	}
	return quotient
}

func lessAddress(a, b [20]byte) bool {
	for i := 0; i < len(a); i++ {
		if a[i] < b[i] {
			return true
		}
		if a[i] > b[i] {
			return false
		}
	}
	return false
}
