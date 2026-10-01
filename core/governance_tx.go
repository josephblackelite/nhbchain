package core

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/governance"
)

// cloneGovernancePolicy deep-copies a governance.ProposalPolicy so a caller
// holding the original can't mutate a StateProcessor's copy out from under
// it -- mirrors cloneLendingRiskParameters/buyback.Config.Clone's existing
// precedent for every other piece of genesis/deployment config threaded
// onto StateProcessor.
func cloneGovernancePolicy(policy governance.ProposalPolicy) governance.ProposalPolicy {
	clone := governance.ProposalPolicy{
		VotingPeriodSeconds:            policy.VotingPeriodSeconds,
		TimelockSeconds:                policy.TimelockSeconds,
		AllowedParams:                  append([]string{}, policy.AllowedParams...),
		QuorumBps:                      policy.QuorumBps,
		PassThresholdBps:               policy.PassThresholdBps,
		AllowedRoles:                   append([]string{}, policy.AllowedRoles...),
		TreasuryAllowList:              append([][20]byte{}, policy.TreasuryAllowList...),
		BlockTimestampToleranceSeconds: policy.BlockTimestampToleranceSeconds,
	}
	if policy.MinDepositWei != nil {
		clone.MinDepositWei = new(big.Int).Set(policy.MinDepositWei)
	}
	return clone
}

// SetGovernancePolicy applies the deployment-configured governance policy to
// this StateProcessor -- called at construction (mirroring
// SetLendingRiskParameters) so every validator's StateProcessor applies
// TxTypeGovXxx transactions against the identical policy, deterministically.
func (sp *StateProcessor) SetGovernancePolicy(policy governance.ProposalPolicy) {
	if sp == nil {
		return
	}
	sp.govPolicy = cloneGovernancePolicy(policy)
}

// SetGovCreditPreservationActivationHeight configures the height at and
// above which applyGovFinalizeTransaction/applyGovExecuteTransaction reload
// the sender's account fresh (sp.getAccount) immediately before the
// nonce-bump persist, instead of reusing the pre-engine-call senderAccount
// snapshot handed in by executeTransaction.
//
// Bug this gates (ledger id PL-R1-GOVREFUND): engine.Finalize/engine.Execute
// (native/governance/engine.go) can write a balance credit directly into
// state for the proposal's credited address -- a deposit refund, a
// forfeited-deposit sweep, or a treasury-directive payout. Both apply
// functions then unconditionally did senderAccount.Nonce++;
// sp.setAccount(sender, senderAccount) using the CALLER's pre-call account
// snapshot. Whenever the sender/caller IS the credited address (e.g.
// finalizing or executing your own proposal), that stale-snapshot persist
// silently erases whatever credit the engine just wrote to that same
// address. applyGovProposeTransaction already avoids the equivalent
// mistake by reloading via sp.incrementNativeAccountNonce after
// SubmitProposal's own debit (see its comment above); this activation
// height lets Finalize/Execute adopt the identical reload-fresh pattern.
// A core/znhb_treasury_pool.go introduced on other, not-yet-merged
// hardening branches (see commit a2c8984, "Book every ZNHB movement on the
// treasury wallet into the Reward Pool in the same state transition" --
// that file does not exist on this branch/origin/main) books net-ZNHB
// movement on the treasury wallet into the Reward Pool; per its own
// commentary that mitigates only this mechanism's CHAIN-HALT side effect
// (the supply invariant tripping on a silently-dropped credit) and does not
// restore the user's lost credit, which is what this activation height's
// gated branch fixes.
//
// This is gated rather than applied unconditionally because the chain's
// first two governance proposals were reportedly self-proposed-and-finalized
// historically, and nothing in this repository (no replay tooling, no
// ledger/runbook record of those specific proposals' kinds or amounts) lets
// that be conclusively confirmed one way or the other as safe to silently
// change. Mirrors Node.quorumCertActivationHeight's gating shape exactly:
// below this height, the original (buggy) persist runs, so any
// already-committed history -- including those two proposals, whatever
// they did -- keeps replaying to the identical state root it always has.
// At or above it, the fixed, reload-fresh persist runs. NewStateProcessor
// defaults this to math.MaxUint64 (disabled -- every existing caller,
// including the whole test suite, keeps today's behavior unchanged).
// Enabling this is an explicit, deploy-time decision, not automatic: it
// must be set to the chain's current tip height, identically on every
// validator, as part of one coordinated upgrade -- deliberately NOT
// auto-detected from chain state, for the same reason
// SetQuorumCertActivationHeight's doc comment gives.
func (sp *StateProcessor) SetGovCreditPreservationActivationHeight(height uint64) {
	if sp == nil {
		return
	}
	sp.govCreditPreservationActivationHeight = height
}

// persistGovActorNonce bumps and persists the governance actor's (sender's)
// nonce after a successful engine.Finalize/engine.Execute call. Below
// sp.govCreditPreservationActivationHeight it reuses the pre-call
// senderAccount snapshot (the original behavior, preserved for
// already-committed history); at or above it, it reloads the account fresh
// first so a same-address credit the engine just wrote survives. See
// SetGovCreditPreservationActivationHeight's doc comment for the full
// rationale.
func (sp *StateProcessor) persistGovActorNonce(sender []byte, senderAccount *types.Account, opName string) error {
	if sp.blockHeight() >= sp.govCreditPreservationActivationHeight {
		fresh, err := sp.getAccount(sender)
		if err != nil {
			return fmt.Errorf("%s: reload sender for nonce persist: %w", opName, err)
		}
		fresh.Nonce++
		if err := sp.setAccount(sender, fresh); err != nil {
			return fmt.Errorf("%s: persist sender nonce: %w", opName, err)
		}
		return nil
	}
	senderAccount.Nonce++
	if err := sp.setAccount(sender, senderAccount); err != nil {
		return fmt.Errorf("%s: persist sender nonce: %w", opName, err)
	}
	return nil
}

// governanceEngine constructs a fresh governance.Engine wired against this
// StateProcessor's trie, event stream, and deployment policy -- mirroring
// Node.newGovernanceEngine (core/node.go), but for use from within
// transaction application rather than a direct RPC call. Critically wires
// SetNowFunc to sp.blockTimestamp(): governance.NewEngine defaults nowFn to
// real wall-clock time, which Node.newGovernanceEngine's RPC-only callers
// never overrode because a single validator's local, non-consensus-routed
// RPC call never needed cross-validator agreement on "now". Once these
// actions are real transactions applied identically by every validator
// (the whole point of this fix), wall-clock nowFn would silently reintroduce
// the exact state-root-mismatch failure mode this session already found and
// fixed for staking's claim-eligibility checks (core/state_transition.go's
// sp.now() -> sp.blockTimestamp() fixes) -- Finalize/Execute's "has the
// voting/timelock period elapsed" checks are exactly as consensus-critical.
func (sp *StateProcessor) governanceEngine() *governance.Engine {
	manager := nhbstate.NewManager(sp.Trie)
	engine := governance.NewEngine()
	engine.SetState(manager)
	engine.SetEmitter(governanceEventEmitter{state: sp})
	engine.SetPolicy(sp.govPolicy)
	engine.SetNowFunc(func() time.Time { return sp.blockTimestamp().UTC() })
	engine.SetAdminWallet(sp.adminWallet, sp.hasAdminWallet)
	return engine
}

// applyGovProposeTransaction handles TxTypeGovPropose. Unlike the RPC
// methods it replaces (Node.GovernancePropose, removed), the proposer
// address comes from the transaction's own cryptographically-recovered
// signer (sender), never from a client-supplied payload field -- closing
// the hole where any RPC caller could previously name ANY address as the
// proposer of a deposit-funded proposal with zero proof of key possession.
func (sp *StateProcessor) applyGovProposeTransaction(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	var payload struct {
		Kind    string
		Payload string
		Deposit *big.Int
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("govPropose: decode payload: %w", err)
	}
	kind := strings.TrimSpace(payload.Kind)
	if kind == "" {
		return fmt.Errorf("govPropose: kind required")
	}
	deposit := big.NewInt(0)
	if payload.Deposit != nil {
		if payload.Deposit.Sign() < 0 {
			return fmt.Errorf("govPropose: deposit must not be negative")
		}
		deposit = new(big.Int).Set(payload.Deposit)
	}

	var proposer [20]byte
	copy(proposer[:], sender)

	// SubmitProposal independently loads and persists the proposer's own
	// account to debit the deposit (native/governance/engine.go) -- call it
	// BEFORE touching the nonce ourselves and re-load fresh afterward
	// (sp.incrementNativeAccountNonce does exactly this), so our nonce-only
	// persist lands on top of its debit rather than racing/clobbering it
	// with this handler's stale pre-debit copy of senderAccount.
	engine := sp.governanceEngine()
	if _, err := engine.SubmitProposal(proposer, kind, payload.Payload, deposit); err != nil {
		return fmt.Errorf("govPropose: %w", err)
	}

	if err := sp.incrementNativeAccountNonce(sender); err != nil {
		return fmt.Errorf("govPropose: persist sender nonce: %w", err)
	}
	return nil
}

// applyGovVoteTransaction handles TxTypeGovVote. As with propose, the voter
// address comes from the transaction's recovered signer, never a payload
// field -- previously, any RPC caller could cast a vote consuming ANY
// address's entire POTSO-weighted voting power by simply naming it as
// `from`.
func (sp *StateProcessor) applyGovVoteTransaction(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	var payload struct {
		ProposalID uint64
		Choice     string
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("govVote: decode payload: %w", err)
	}
	choice := strings.TrimSpace(payload.Choice)
	if choice == "" {
		return fmt.Errorf("govVote: choice required")
	}

	var voter [20]byte
	copy(voter[:], sender)

	engine := sp.governanceEngine()
	if err := engine.CastVote(payload.ProposalID, voter, choice); err != nil {
		return fmt.Errorf("govVote: %w", err)
	}

	senderAccount.Nonce++
	if err := sp.setAccount(sender, senderAccount); err != nil {
		return fmt.Errorf("govVote: persist sender nonce: %w", err)
	}
	return nil
}

// applyGovFinalizeTransaction handles TxTypeGovFinalize. Finalize has no
// caller-identity concept at the engine level (native/governance/engine.go's
// Finalize takes only a proposal ID) -- it is a pure "the voting period has
// elapsed, compute and record the outcome" trigger, deterministic given the
// proposal's own VotingEnd and the current block timestamp. Requiring a real
// signed envelope here (rather than the old JWT-only-gated RPC call) ties
// every finalize action to a real, nonce-tracked, fee-paying account instead
// of any bearer-token holder, and -- the primary fix -- makes the action a
// real transaction every validator applies identically instead of a
// per-validator local trie write.
func (sp *StateProcessor) applyGovFinalizeTransaction(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	var payload struct {
		ProposalID uint64
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("govFinalize: decode payload: %w", err)
	}

	engine := sp.governanceEngine()
	if _, _, err := engine.Finalize(payload.ProposalID); err != nil {
		return fmt.Errorf("govFinalize: %w", err)
	}

	return sp.persistGovActorNonce(sender, senderAccount, "govFinalize")
}

// applyGovQueueTransaction handles TxTypeGovQueue -- same shape and
// rationale as applyGovFinalizeTransaction; QueueExecution is likewise a
// pure "proposal passed, mark it queued" trigger with no caller-identity
// concept at the engine level.
func (sp *StateProcessor) applyGovQueueTransaction(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	var payload struct {
		ProposalID uint64
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("govQueue: decode payload: %w", err)
	}

	engine := sp.governanceEngine()
	if err := engine.QueueExecution(payload.ProposalID); err != nil {
		return fmt.Errorf("govQueue: %w", err)
	}

	senderAccount.Nonce++
	if err := sp.setAccount(sender, senderAccount); err != nil {
		return fmt.Errorf("govQueue: persist sender nonce: %w", err)
	}
	return nil
}

// applyGovExecuteTransaction handles TxTypeGovExecute -- same shape and
// rationale as applyGovFinalizeTransaction/applyGovQueueTransaction; Execute
// applies an already-passed, already-timelocked proposal's payload (see
// native/governance/engine.go's Execute for the per-kind dispatch), with no
// caller-identity concept at the engine level.
func (sp *StateProcessor) applyGovExecuteTransaction(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	var payload struct {
		ProposalID uint64
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("govExecute: decode payload: %w", err)
	}

	engine := sp.governanceEngine()
	if err := engine.Execute(payload.ProposalID); err != nil {
		return fmt.Errorf("govExecute: %w", err)
	}

	return sp.persistGovActorNonce(sender, senderAccount, "govExecute")
}
