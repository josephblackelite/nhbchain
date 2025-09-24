package epoch

import (
	"encoding/hex"
	"math/big"
	"sort"
)

// Weight captures the composite weight calculation inputs and result for a
// single validator.
type Weight struct {
	Address    []byte
	Stake      *big.Int
	Engagement uint64
	Composite  *big.Int
}

// Snapshot stores the per-epoch weight calculations and the resulting validator
// selection.
type Snapshot struct {
	Epoch       uint64
	Height      uint64
	FinalizedAt int64
	TotalWeight *big.Int
	Weights     []Weight
	Selected    [][]byte
}

// Summary provides a lightweight view over an epoch for external consumers.
type Summary struct {
	Epoch            uint64
	Height           uint64
	FinalizedAt      int64
	TotalWeight      *big.Int
	ActiveValidators [][]byte
	EligibleCount    int
}

// Summary converts a snapshot into its summary representation.
func (s Snapshot) Summary() Summary {
	selected := make([][]byte, len(s.Selected))
	for i := range s.Selected {
		selected[i] = append([]byte(nil), s.Selected[i]...)
	}
	return Summary{
		Epoch:            s.Epoch,
		Height:           s.Height,
		FinalizedAt:      s.FinalizedAt,
		TotalWeight:      new(big.Int).Set(s.TotalWeight),
		ActiveValidators: selected,
		EligibleCount:    len(s.Weights),
	}
}

// ComputeCompositeWeight derives the composite weight for a validator.
func ComputeCompositeWeight(cfg Config, stake *big.Int, engagement uint64) *big.Int {
	weight := new(big.Int)
	if stake != nil && cfg.StakeWeight > 0 {
		component := new(big.Int).Set(stake)
		component.Mul(component, new(big.Int).SetUint64(cfg.StakeWeight))
		weight.Add(weight, component)
	}
	if cfg.EngagementWeight > 0 && engagement > 0 {
		component := new(big.Int).SetUint64(engagement)
		component.Mul(component, new(big.Int).SetUint64(cfg.EngagementWeight))
		weight.Add(weight, component)
	}
	return weight
}

// SortWeights sorts weights by descending composite weight with a deterministic
// tie-breaker on address bytes.
func SortWeights(weights []Weight) {
	sort.Slice(weights, func(i, j int) bool {
		if weights[i].Composite == nil && weights[j].Composite == nil {
			return hex.EncodeToString(weights[i].Address) < hex.EncodeToString(weights[j].Address)
		}
		if weights[i].Composite == nil {
			return false
		}
		if weights[j].Composite == nil {
			return true
		}
		cmp := weights[i].Composite.Cmp(weights[j].Composite)
		if cmp == 0 {
			return hex.EncodeToString(weights[i].Address) < hex.EncodeToString(weights[j].Address)
		}
		return cmp > 0
	})
}
