package potso

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

const (
	// RewardBpsDenominator defines the fixed denominator used for alpha weighting.
	RewardBpsDenominator = 10000
)

// RewardPayoutMode enumerates the settlement strategies supported by the reward module.
type RewardPayoutMode string

const (
	// RewardPayoutModeAuto transfers rewards automatically at epoch roll-over.
	RewardPayoutModeAuto RewardPayoutMode = "auto"
	// RewardPayoutModeClaim records claimable rewards that must be explicitly settled by the winner.
	RewardPayoutModeClaim RewardPayoutMode = "claim"
)

// Valid reports whether the payout mode is recognised.
func (m RewardPayoutMode) Valid() bool {
	switch strings.ToLower(string(m)) {
	case string(RewardPayoutModeAuto), string(RewardPayoutModeClaim):
		return true
	default:
		return false
	}
}

// Normalise returns the canonical representation of the payout mode, defaulting to auto when unset.
func (m RewardPayoutMode) Normalise() RewardPayoutMode {
	switch strings.ToLower(strings.TrimSpace(string(m))) {
	case string(RewardPayoutModeClaim):
		return RewardPayoutModeClaim
	default:
		return RewardPayoutModeAuto
	}
}

var (
	// ErrRewardNotFound indicates that no claim exists for the requested epoch + address pair.
	ErrRewardNotFound = errors.New("potso: reward not found")
	// ErrRewardAlreadyClaimed signals that the reward has been previously settled.
	ErrRewardAlreadyClaimed = errors.New("potso: reward already claimed")
	// ErrInsufficientTreasury is raised when the treasury balance is insufficient to cover a reward.
	ErrInsufficientTreasury = errors.New("potso: insufficient treasury balance")
	// ErrClaimingDisabled denotes that manual claiming is not enabled for the current configuration.
	ErrClaimingDisabled = errors.New("potso: reward claiming disabled")
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
	PayoutMode         RewardPayoutMode
	MaxUserShareBps    uint64
}

// RewardSnapshotEntry captures the raw inputs for a participant when computing
// epoch rewards.
type RewardSnapshotEntry struct {
	Address            [20]byte
	Stake              *big.Int
	Meter              EngagementMeter
	PreviousEngagement uint64
}

// RewardSnapshot aggregates the inputs used to derive payouts for an epoch.
type RewardSnapshot struct {
	Epoch   uint64
	Day     string
	Entries []RewardSnapshotEntry
}

// RewardPayout captures the computed payout for a participant.
type RewardPayout struct {
	Address [20]byte
	Amount  *big.Int
	Weight  *big.Rat
}

// RewardOutcome summarises the distribution for a single epoch.
type RewardOutcome struct {
	Epoch          uint64
	Budget         *big.Int
	TotalPaid      *big.Int
	Remainder      *big.Int
	Winners        []RewardPayout
	WeightSnapshot *WeightSnapshot
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
		PayoutMode:         RewardPayoutModeAuto,
		MaxUserShareBps:    0,
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
	if c.EpochLengthBlocks > 0 {
		if c.EmissionPerEpoch == nil || c.EmissionPerEpoch.Sign() <= 0 {
			return errors.New("emission per epoch must be positive when epoch length is set")
		}
	}
	if c.MaxUserShareBps > RewardBpsDenominator {
		return fmt.Errorf("max user share must be <= %d", RewardBpsDenominator)
	}
	mode := c.PayoutMode.Normalise()
	if !mode.Valid() {
		return fmt.Errorf("unsupported payout mode %q", c.PayoutMode)
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

// EffectivePayoutMode returns the configured payout mode, defaulting to auto for unset values.
func (c RewardConfig) EffectivePayoutMode() RewardPayoutMode {
	return c.PayoutMode.Normalise()
}

// RewardClaim records the settlement status for a participant within an epoch.
type RewardClaim struct {
	Amount    *big.Int
	Claimed   bool
	ClaimedAt uint64
	Mode      RewardPayoutMode
}

// Clone creates a deep copy of the claim structure.
func (c *RewardClaim) Clone() *RewardClaim {
	if c == nil {
		return nil
	}
	clone := &RewardClaim{
		Claimed:   c.Claimed,
		ClaimedAt: c.ClaimedAt,
		Mode:      c.Mode,
	}
	if c.Amount != nil {
		clone.Amount = new(big.Int).Set(c.Amount)
	} else {
		clone.Amount = big.NewInt(0)
	}
	return clone
}

// RewardHistoryEntry captures a single payout that has been settled for an address.
type RewardHistoryEntry struct {
	Epoch  uint64
	Amount *big.Int
	Mode   RewardPayoutMode
}

// Clone returns a deep copy of the history entry.
func (e *RewardHistoryEntry) Clone() RewardHistoryEntry {
	out := RewardHistoryEntry{Epoch: e.Epoch, Mode: e.Mode}
	if e.Amount != nil {
		out.Amount = new(big.Int).Set(e.Amount)
	} else {
		out.Amount = big.NewInt(0)
	}
	return out
}

// ComputeRewards derives the payouts for a snapshot given the configured budget and weight parameters.
func ComputeRewards(cfg RewardConfig, params WeightParams, snapshot RewardSnapshot, budget *big.Int) (*RewardOutcome, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := params.Validate(); err != nil {
		return nil, err
	}
	if budget == nil {
		budget = big.NewInt(0)
	}
	result := &RewardOutcome{
		Epoch:          snapshot.Epoch,
		Budget:         copyBigInt(budget),
		TotalPaid:      big.NewInt(0),
		Remainder:      copyBigInt(budget),
		Winners:        []RewardPayout{},
		WeightSnapshot: nil,
	}
	if budget.Sign() <= 0 {
		return result, nil
	}

	inputs := make([]WeightInput, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		inputs = append(inputs, WeightInput{
			Address:            entry.Address,
			Stake:              copyBigInt(entry.Stake),
			PreviousEngagement: entry.PreviousEngagement,
			Meter:              entry.Meter,
		})
	}
	weights, err := ComputeWeightSnapshot(snapshot.Epoch, inputs, params)
	if err != nil {
		return nil, err
	}
	result.WeightSnapshot = weights
	if weights == nil || len(weights.Entries) == 0 {
		return result, nil
	}

	weighted := make([]RewardPayout, 0, len(weights.Entries))
	for _, entry := range weights.Entries {
		if entry.Weight == nil || entry.Weight.Sign() <= 0 {
			continue
		}
		weighted = append(weighted, RewardPayout{Address: entry.Address, Amount: big.NewInt(0), Weight: new(big.Rat).Set(entry.Weight)})
	}
	if len(weighted) == 0 {
		return result, nil
	}

	if cfg.MaxWinnersPerEpoch > 0 && uint64(len(weighted)) > cfg.MaxWinnersPerEpoch {
		weighted = weighted[:cfg.MaxWinnersPerEpoch]
	}

	type payoutCandidate struct {
		RewardPayout
		cap      *big.Int
		headroom *big.Int
	}

	candidates := make([]payoutCandidate, 0, len(weighted))
	pool := big.NewInt(0)
	var capWei *big.Int
	if cfg.MaxUserShareBps > 0 {
		capRatio := new(big.Rat).SetFrac(big.NewInt(int64(cfg.MaxUserShareBps)), big.NewInt(RewardBpsDenominator))
		capWei = ratMulInt(capRatio, budget)
	}

	for _, candidate := range weighted {
		if candidate.Weight == nil || candidate.Weight.Sign() <= 0 {
			continue
		}
		weightCopy := new(big.Rat).Set(candidate.Weight)
		base := ratMulInt(weightCopy, budget)
		amount := new(big.Int).Set(base)
		var cap *big.Int
		var headroom *big.Int
		if capWei != nil {
			cap = new(big.Int).Set(capWei)
			if amount.Cmp(cap) > 0 {
				amount.Set(cap)
			}
			headroom = new(big.Int).Sub(cap, amount)
			if headroom.Sign() < 0 {
				headroom = big.NewInt(0)
			}
		}
		clipped := new(big.Int).Sub(base, amount)
		if clipped.Sign() > 0 {
			pool.Add(pool, clipped)
		}
		candidates = append(candidates, payoutCandidate{
			RewardPayout: RewardPayout{
				Address: candidate.Address,
				Amount:  amount,
				Weight:  weightCopy,
			},
			cap:      cap,
			headroom: headroom,
		})
	}

	if cfg.MaxUserShareBps > 0 && pool.Sign() > 0 {
		remaining := new(big.Int).Set(pool)
		for remaining.Sign() > 0 {
			indices := make([]int, 0, len(candidates))
			weightSum := new(big.Rat)
			for i := range candidates {
				if candidates[i].headroom != nil && candidates[i].headroom.Sign() > 0 {
					indices = append(indices, i)
					weightSum.Add(weightSum, candidates[i].Weight)
				}
			}
			if len(indices) == 0 {
				break
			}
			allocated := big.NewInt(0)
			for _, idx := range indices {
				share := new(big.Rat).Set(candidates[idx].Weight)
				if weightSum.Sign() > 0 {
					share.Quo(share, weightSum)
				} else {
					share.SetInt64(0)
				}
				allocation := ratMulInt(share, remaining)
				if candidates[idx].headroom.Cmp(allocation) < 0 {
					allocation = new(big.Int).Set(candidates[idx].headroom)
				}
				if allocation.Sign() > 0 {
					candidates[idx].Amount.Add(candidates[idx].Amount, allocation)
					candidates[idx].headroom.Sub(candidates[idx].headroom, allocation)
					allocated.Add(allocated, allocation)
				}
			}
			if allocated.Sign() == 0 {
				for _, idx := range indices {
					if remaining.Sign() == 0 {
						break
					}
					if candidates[idx].headroom.Sign() == 0 {
						continue
					}
					step := big.NewInt(1)
					if candidates[idx].headroom.Cmp(step) < 0 {
						step = new(big.Int).Set(candidates[idx].headroom)
					}
					if remaining.Cmp(step) < 0 {
						step = new(big.Int).Set(remaining)
					}
					candidates[idx].Amount.Add(candidates[idx].Amount, step)
					candidates[idx].headroom.Sub(candidates[idx].headroom, step)
					allocated.Add(allocated, step)
					remaining.Sub(remaining, step)
					if remaining.Sign() == 0 {
						break
					}
				}
			} else {
				remaining.Sub(remaining, allocated)
			}
		}
		pool = remaining
	}

	winners := make([]RewardPayout, 0, len(candidates))
	totalPaid := big.NewInt(0)
	minPayout := cfg.MinPayoutWei
	if minPayout == nil {
		minPayout = big.NewInt(0)
	}
	for _, candidate := range candidates {
		if candidate.Amount == nil || candidate.Amount.Sign() <= 0 {
			continue
		}
		if minPayout.Sign() > 0 && candidate.Amount.Cmp(minPayout) < 0 {
			continue
		}
		winners = append(winners, RewardPayout{
			Address: candidate.Address,
			Amount:  new(big.Int).Set(candidate.Amount),
			Weight:  new(big.Rat).Set(candidate.Weight),
		})
		totalPaid.Add(totalPaid, candidate.Amount)
	}

	remainder := new(big.Int).Sub(budget, totalPaid)
	if remainder.Sign() < 0 {
		remainder = big.NewInt(0)
	}

	result.TotalPaid = totalPaid
	result.Remainder = remainder
	result.Winners = winners

	return result, nil
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
