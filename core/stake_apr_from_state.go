package core

import (
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/rewards"
	nhbstate "nhbchain/core/state"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/governance"
)

// stakeAPRDefaultBps is the reward rate when the chain stores none (or one that cannot
// be read): the same 12.5 percent the staking reward engine (core/state) falls back to.
// A fixed constant, so every node reads the same thing.
const stakeAPRDefaultBps uint64 = 1250

// stakeAPRFromStateActive reports whether the reward rate is read from the chain at the
// block being executed (PL-R5T-27, StakeAPRFromStateActivationHeight).
func (sp *StateProcessor) stakeAPRFromStateActive() bool {
	return sp != nil && ruleActiveAt(sp.validatorRules.StakeAPRFromStateActivationHeight, sp.blockHeight())
}

// stakeAPRFromParam reads staking.aprBps from the governance parameter store: a pure
// function of the chain state. Nothing stored, an empty value, or one that is not a
// whole number that fits 32 bits (the width the rate has everywhere else) is the fixed
// default; only a failure to read the store is an error.
func (sp *StateProcessor) stakeAPRFromParam() (uint64, error) {
	raw, ok, err := nhbstate.NewManager(sp.Trie).ParamStoreGet(governance.ParamKeyStakingAprBps)
	if err != nil {
		return 0, fmt.Errorf("staking: load %s: %w", governance.ParamKeyStakingAprBps, err)
	}
	if !ok {
		return stakeAPRDefaultBps, nil
	}
	trimmed := nativecommon.ParamDecimal(raw)
	if trimmed == "" {
		return stakeAPRDefaultBps, nil
	}
	value, parseErr := strconv.ParseUint(trimmed, 10, 32)
	if parseErr != nil {
		return stakeAPRDefaultBps, nil
	}
	return value, nil
}

// stakeAPRInForce is the rate the claim events report and the next index update uses:
// the chain's from the activation height, the cached one before it. A failure to read
// the store (the node cannot read its own state, which aborts the block anyway) falls
// back to the default.
func (sp *StateProcessor) stakeAPRInForce() uint64 {
	if sp == nil {
		return 0
	}
	if !sp.stakeAPRFromStateActive() {
		return sp.stakeRewardAPR
	}
	apr, err := sp.stakeAPRFromParam()
	if err != nil {
		return stakeAPRDefaultBps
	}
	return apr
}

// persistedStakeAPR is the rate the reward index was last settled at, from the state
// trie and not from this process: found is false when no reward state has been written.
func (sp *StateProcessor) persistedStakeAPR() (apr uint64, found bool, err error) {
	data, err := sp.Trie.Get(stakeRewardStateKey)
	if err != nil {
		return 0, false, err
	}
	if len(data) == 0 {
		return 0, false, nil
	}
	var record stakeRewardRecord
	if err := rlp.DecodeBytes(data, &record); err != nil {
		return 0, false, err
	}
	return record.APRBps, true, nil
}

// settleStakeAPRChange brings the reward index and the persisted rate in line with the
// chain's rate. When the rate the index was last settled at differs from the chain's,
// the index is first advanced to this block's time at the OLD rate, so the interval
// before the change is never repriced, and then the chain's rate is recorded. It does
// nothing before the activation height. The old rate is read from the state trie, never
// from this process, so a node restarted with other config settles the same way.
//
// It runs before every index update and, so that the settlement lands in the block that
// made the change rather than at the next staking transaction, after a governance
// execution.
func (sp *StateProcessor) settleStakeAPRChange() error {
	if !sp.stakeAPRFromStateActive() {
		return nil
	}
	chain, err := sp.stakeAPRFromParam()
	if err != nil {
		return err
	}
	prev, found, err := sp.persistedStakeAPR()
	if err != nil {
		return fmt.Errorf("staking: load reward state: %w", err)
	}
	if !found || prev == chain {
		sp.stakeRewardAPR = chain
		return nil
	}
	if sp.stakeRewardEngine == nil {
		sp.stakeRewardEngine = rewards.NewEngine()
	}
	sp.stakeRewardEngine.UpdateGlobalIndex(sp.blockTimestamp(), prev)
	sp.stakeRewardAPR = chain
	return sp.persistStakeRewardStateInstrumented()
}
