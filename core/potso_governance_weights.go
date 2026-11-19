package core

import (
	"bytes"
	"fmt"
	"math/big"
	"sort"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/potso"
)

// Governance voting weight for delegators (ledger PL-CLM-04).
//
// A vote's power is the voter's entry in the stored POTSO weight snapshot of the
// last processed epoch: 70 percent the share of all counted stake, 30 percent the
// share of all counted engagement. A validator's counted stake is its whole
// account stake, which by design includes everything delegated to it, and a
// delegator's own stake reads zero, so the people who own the locked ZNHB hold no
// weight and the validator's operator holds all of it. On the live chain the
// snapshot has two entries, the two validators, 3,500 basis points each (the other
// 3,000 is the engagement share, which nobody has), and the owners of the
// 20,010,000 ZNHB delegated to them cannot vote.
//
// From potsoGovernanceWeightFromHeight (zero = off, the live value) the stored
// snapshot that governance reads is built separately from the one that pays
// rewards, which stays exactly as it was:
//
//   - Stake follows ownership. For every eligible validator, each delegator's
//     locked ZNHB moves out of the validator's counted stake into the delegator's
//     (never more than the validator holds). The total counted stake does not
//     change, so nothing is counted twice; a validator whose stake is all delegated
//     keeps only what it owns, and its delegators vote in its place.
//   - The snapshot is built every processed epoch, not only when the epoch's reward
//     budget is positive. Today an epoch with an empty reward treasury stores no
//     snapshot and every vote fails with "potso snapshot unavailable".
//
// Engagement is deliberately not changed. An account that transacts has engagement
// weight (30 percent of the total is shared by engagement) while its engagement
// lasts, and it lasts about half an hour: the average has a half-life of seven
// epochs of 120 blocks. A longer half-life does not work with the integer average
// the weights use (applyEMA): one active epoch adds only a few units, and every
// epoch after rounds down, so a small value is gone in as many epochs as it has
// units, whatever the half-life. Weight for accounts that were active over days
// needs a different measure (for example a count of active days in a window, kept
// per account), which is an owner decision; this file does not guess it.

// SetPotsoGovernanceWeights sets the first block height from which governance
// reads its own weight snapshot. A zero height leaves everything as it was. A
// chain rule: the same value on every validator, chosen above the chain tip, all
// restarted together.
func (sp *StateProcessor) SetPotsoGovernanceWeights(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.potsoGovernanceWeightFromHeight = fromHeight
}

// potsoGovernanceWeightActive reports whether governance reads its own snapshot
// for the epoch being processed in the block being executed.
func (sp *StateProcessor) potsoGovernanceWeightActive() bool {
	if sp == nil || sp.potsoGovernanceWeightFromHeight == 0 {
		return false
	}
	return sp.blockHeight() >= sp.potsoGovernanceWeightFromHeight
}

// governanceWeightSnapshot builds the weight snapshot governance reads for epoch
// from the reward snapshot's entries (see the comment at the top of this file).
func (sp *StateProcessor) governanceWeightSnapshot(manager *nhbstate.Manager, epoch uint64, entries []potso.RewardSnapshotEntry, params potso.WeightParams) (*potso.StoredWeightSnapshot, error) {
	stakes := make(map[[20]byte]*big.Int, len(entries))
	for _, entry := range entries {
		stake := big.NewInt(0)
		if entry.Stake != nil {
			stake = new(big.Int).Set(entry.Stake)
		}
		stakes[entry.Address] = stake
	}

	// Stake follows ownership. Validators and delegators are taken in sorted order
	// so the result never depends on map iteration.
	validators := make([][20]byte, 0, len(sp.EligibleValidators))
	for key, basis := range sp.EligibleValidators {
		if len(key) != 20 || basis == nil || basis.Sign() <= 0 {
			continue
		}
		var v [20]byte
		copy(v[:], key)
		validators = append(validators, v)
	}
	sort.Slice(validators, func(i, j int) bool { return bytes.Compare(validators[i][:], validators[j][:]) < 0 })
	eligible := make(map[[20]byte]struct{}, len(validators))
	for _, v := range validators {
		eligible[v] = struct{}{}
	}
	for _, v := range validators {
		delegators, err := manager.StakeValidatorDelegators(v)
		if err != nil {
			return nil, fmt.Errorf("potso: governance weights: delegators of validator: %w", err)
		}
		sort.Slice(delegators, func(i, j int) bool { return bytes.Compare(delegators[i][:], delegators[j][:]) < 0 })
		for _, d := range delegators {
			if d == v {
				continue
			}
			// A delegator that is itself an eligible validator already has its locked
			// amount counted as its own stake: leave both sides as they were.
			if _, ok := eligible[d]; ok {
				continue
			}
			account, err := manager.GetAccount(d[:])
			if err != nil {
				return nil, fmt.Errorf("potso: governance weights: delegator account: %w", err)
			}
			if account == nil || account.LockedZNHB == nil || account.LockedZNHB.Sign() <= 0 {
				continue
			}
			if !bytes.Equal(account.DelegatedValidator, v[:]) {
				continue
			}
			held, ok := stakes[v]
			if !ok || held.Sign() <= 0 {
				continue
			}
			moved := new(big.Int).Set(account.LockedZNHB)
			if moved.Cmp(held) > 0 {
				moved.Set(held)
			}
			stakes[v] = new(big.Int).Sub(held, moved)
			if current, ok := stakes[d]; ok {
				stakes[d] = new(big.Int).Add(current, moved)
			} else {
				stakes[d] = new(big.Int).Set(moved)
			}
		}
	}

	inputs := make([]potso.WeightInput, 0, len(entries)+len(stakes))
	seen := make(map[[20]byte]struct{}, len(stakes))
	for _, entry := range entries {
		seen[entry.Address] = struct{}{}
		inputs = append(inputs, potso.WeightInput{
			Address:            entry.Address,
			Stake:              new(big.Int).Set(stakes[entry.Address]),
			PreviousEngagement: entry.PreviousEngagement,
			Meter:              entry.Meter,
		})
	}
	// Delegators that were not in the reward snapshot at all.
	extra := make([][20]byte, 0)
	for addr := range stakes {
		if _, ok := seen[addr]; !ok {
			extra = append(extra, addr)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return bytes.Compare(extra[i][:], extra[j][:]) < 0 })
	for _, addr := range extra {
		inputs = append(inputs, potso.WeightInput{Address: addr, Stake: new(big.Int).Set(stakes[addr])})
	}

	weights, err := potso.ComputeWeightSnapshot(epoch, inputs, params)
	if err != nil {
		return nil, fmt.Errorf("potso: governance weights: %w", err)
	}
	return weights.ToStored(), nil
}
