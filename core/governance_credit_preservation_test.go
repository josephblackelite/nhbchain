package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
)

// This file is the regression coverage for PL-R1-GOVREFUND: see
// core/governance_tx.go's SetGovCreditPreservationActivationHeight doc
// comment for the full bug writeup. In short, applyGovFinalizeTransaction
// and applyGovExecuteTransaction call engine.Finalize/engine.Execute, which
// can credit a balance directly onto the proposal's credited address (a
// deposit refund, a forfeited-deposit sweep, or a treasury-directive
// payout) -- but both functions then persisted the CALLER's pre-engine-call
// account snapshot, silently erasing that credit whenever the caller IS the
// credited address (self-finalizing or self-executing your own proposal).
// Both tests below drive the real signed-transaction path
// (sp.ApplyTransaction), never a direct governance.Engine call, and prove
// the fix's activation-height gate behaves exactly as documented: disabled
// (the default, and any height below the configured activation height)
// reproduces the original behavior so already-committed history keeps
// replaying to the identical state root; at or above the activation height,
// the credit survives.

// TestApplyGovFinalizeTransaction_SelfFinalizePreservesDepositRefund covers
// engine.Finalize's PASSED-branch deposit refund: Finalize credits
// proposal.Submitter directly. When the account submitting the
// TxTypeGovFinalize transaction IS that submitter (finalizing your own
// proposal), the refund must survive applyGovFinalizeTransaction's
// subsequent nonce bump and persist.
func TestApplyGovFinalizeTransaction_SelfFinalizePreservesDepositRefund(t *testing.T) {
	const activationHeight = 100
	deposit := weiAmount(1_000)

	cases := []struct {
		name        string
		setGate     bool
		blockHeight uint64
		wantRefund  bool
	}{
		{"DefaultDisabledKeepsHistoricalBehavior", false, 10_000_000, false},
		{"BelowActivationHeightKeepsHistoricalBehavior", true, activationHeight - 1, false},
		{"AtActivationHeightPreservesCredit", true, activationHeight, true},
		{"AboveActivationHeightPreservesCredit", true, activationHeight + 50, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := newZNHBPoolsStateProcessor(t)
			if tc.setGate {
				sp.SetGovCreditPreservationActivationHeight(activationHeight)
			}

			now := time.Unix(1_900_000_000, 0).UTC()
			sp.BeginBlock(tc.blockHeight, now)

			sp.SetGovernancePolicy(governance.ProposalPolicy{
				MinDepositWei:       big.NewInt(0),
				VotingPeriodSeconds: 60,
				TimelockSeconds:     10,
				AllowedParams:       []string{"fees.baseFee"},
				QuorumBps:           0,
				PassThresholdBps:    0,
			})

			key, err := crypto.GeneratePrivateKey()
			if err != nil {
				t.Fatalf("generate proposer/finalizer key: %v", err)
			}
			addr := key.PubKey().Address().Bytes()
			if err := sp.setAccount(addr, &types.Account{BalanceZNHB: new(big.Int).Set(deposit)}); err != nil {
				t.Fatalf("seed proposer/finalizer account: %v", err)
			}

			proposeTx := signedGovTx(t, key, 0, types.TxTypeGovPropose, govProposePayload{
				Kind:    governance.ProposalKindParamUpdate,
				Payload: `{"fees.baseFee":1000}`,
				Deposit: deposit,
			})
			if err := sp.ApplyTransaction(proposeTx); err != nil {
				t.Fatalf("propose: %v", err)
			}
			proposalID := latestGovProposalID(t, sp)

			debited, err := sp.getAccount(addr)
			if err != nil {
				t.Fatalf("load account after propose: %v", err)
			}
			if debited.BalanceZNHB.Sign() != 0 {
				t.Fatalf("expected the deposit to be debited at propose time, got balance=%s", debited.BalanceZNHB)
			}

			// Advance past VotingEnd (VotingPeriodSeconds: 60) within the same
			// gated block height -- only the timestamp needs to move so
			// Finalize sees the voting period elapsed; blockHeight stays
			// exactly what this subtest is proving the gate against.
			now = now.Add(2 * time.Minute)
			sp.BeginBlock(tc.blockHeight, now)

			finalizeTx := signedGovTx(t, key, 1, types.TxTypeGovFinalize, govProposalIDPayload{ProposalID: proposalID})
			if err := sp.ApplyTransaction(finalizeTx); err != nil {
				t.Fatalf("finalize: %v", err)
			}

			manager := nhbstate.NewManager(sp.Trie)
			proposal, ok, err := manager.GovernanceGetProposal(proposalID)
			if err != nil || !ok {
				t.Fatalf("load proposal after finalize: ok=%v err=%v", ok, err)
			}
			if proposal.Status != governance.ProposalStatusPassed {
				t.Fatalf("expected the proposal to pass (QuorumBps/PassThresholdBps 0), got %v", proposal.Status)
			}

			account, err := sp.getAccount(addr)
			if err != nil {
				t.Fatalf("load finalizer account after finalize: %v", err)
			}
			if account.Nonce != 2 {
				t.Fatalf("expected the finalizer's nonce bumped to 2 regardless of the gate, got %d", account.Nonce)
			}
			if tc.wantRefund {
				if account.BalanceZNHB.Cmp(deposit) != 0 {
					t.Fatalf("expected the self-finalizer's deposit refund to survive the nonce-bump persist, got balance=%s want=%s", account.BalanceZNHB, deposit)
				}
			} else if account.BalanceZNHB.Sign() != 0 {
				t.Fatalf("expected the historical (pre-activation) behavior to still silently drop the refund (PL-R1-GOVREFUND unfixed below the gate), got balance=%s want=0 -- the gate changed already-committed-equivalent behavior it must not touch", account.BalanceZNHB)
			}
		})
	}
}

// TestApplyGovExecuteTransaction_SelfExecutePreservesTreasuryPayout covers
// engine.Execute's ProposalKindTreasuryDirective payout: applyTreasuryDirective
// credits each transfer's recipient directly. When the account submitting
// the TxTypeGovExecute transaction IS one of those recipients (executing a
// treasury directive that pays yourself), the payout must survive
// applyGovExecuteTransaction's subsequent nonce bump and persist.
func TestApplyGovExecuteTransaction_SelfExecutePreservesTreasuryPayout(t *testing.T) {
	const activationHeight = 100
	payout := weiAmount(500)
	treasuryFunding := weiAmount(10_000)

	cases := []struct {
		name        string
		setGate     bool
		blockHeight uint64
		wantPayout  bool
	}{
		{"DefaultDisabledKeepsHistoricalBehavior", false, 10_000_000, false},
		{"BelowActivationHeightKeepsHistoricalBehavior", true, activationHeight - 1, false},
		{"AtActivationHeightPreservesCredit", true, activationHeight, true},
		{"AboveActivationHeightPreservesCredit", true, activationHeight + 50, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := newZNHBPoolsStateProcessor(t)
			if tc.setGate {
				sp.SetGovCreditPreservationActivationHeight(activationHeight)
			}

			now := time.Unix(1_900_000_000, 0).UTC()
			sp.BeginBlock(tc.blockHeight, now)

			sourceKey, err := crypto.GeneratePrivateKey()
			if err != nil {
				t.Fatalf("generate treasury source key: %v", err)
			}
			var sourceAddr [20]byte
			copy(sourceAddr[:], sourceKey.PubKey().Address().Bytes())
			if err := sp.setAccount(sourceAddr[:], &types.Account{BalanceZNHB: new(big.Int).Set(treasuryFunding)}); err != nil {
				t.Fatalf("seed treasury source account: %v", err)
			}

			sp.SetGovernancePolicy(governance.ProposalPolicy{
				MinDepositWei:       big.NewInt(0),
				VotingPeriodSeconds: 60,
				TimelockSeconds:     10,
				QuorumBps:           0,
				PassThresholdBps:    0,
				TreasuryAllowList:   [][20]byte{sourceAddr},
			})

			// executorKey both submits the Execute transaction AND is the
			// treasury directive's payout recipient -- the self-payout
			// scenario. A fresh, never-seen key starts at nonce 0 and a lazy
			// zero-initialized BalanceZNHB, so there is nothing to seed.
			executorKey, err := crypto.GeneratePrivateKey()
			if err != nil {
				t.Fatalf("generate executor key: %v", err)
			}
			executorAddr := executorKey.PubKey().Address().Bytes()

			proposerKey := freshGovAccountKey(t, sp, big.NewInt(0))
			payload := `{"source":"` + crypto.MustNewAddress(crypto.NHBPrefix, sourceAddr[:]).String() +
				`","transfers":[{"to":"` + crypto.MustNewAddress(crypto.NHBPrefix, executorAddr).String() +
				`","amountWei":"` + payout.String() + `"}]}`
			applySignedGovTx(t, sp, proposerKey, types.TxTypeGovPropose, govProposePayload{
				Kind:    governance.ProposalKindTreasuryDirective,
				Payload: payload,
				Deposit: big.NewInt(0),
			})
			proposalID := latestGovProposalID(t, sp)

			markGovProposalPassed(t, sp, proposalID)

			queueKey := freshGovAccountKey(t, sp, big.NewInt(0))
			applySignedGovTx(t, sp, queueKey, types.TxTypeGovQueue, govProposalIDPayload{ProposalID: proposalID})

			clearGovProposalTimelock(t, sp, proposalID)

			executeTx := signedGovTx(t, executorKey, 0, types.TxTypeGovExecute, govProposalIDPayload{ProposalID: proposalID})
			if err := sp.ApplyTransaction(executeTx); err != nil {
				t.Fatalf("execute: %v", err)
			}

			manager := nhbstate.NewManager(sp.Trie)
			proposal, ok, err := manager.GovernanceGetProposal(proposalID)
			if err != nil || !ok {
				t.Fatalf("load proposal after execute: ok=%v err=%v", ok, err)
			}
			if proposal.Status != governance.ProposalStatusExecuted {
				t.Fatalf("expected the proposal to be executed, got %v", proposal.Status)
			}

			account, err := sp.getAccount(executorAddr)
			if err != nil {
				t.Fatalf("load executor account after execute: %v", err)
			}
			if account.Nonce != 1 {
				t.Fatalf("expected the executor's nonce bumped to 1 regardless of the gate, got %d", account.Nonce)
			}
			if tc.wantPayout {
				if account.BalanceZNHB.Cmp(payout) != 0 {
					t.Fatalf("expected the self-executor's treasury payout to survive the nonce-bump persist, got balance=%s want=%s", account.BalanceZNHB, payout)
				}
			} else if account.BalanceZNHB.Sign() != 0 {
				t.Fatalf("expected the historical (pre-activation) behavior to still silently drop the payout (PL-R1-GOVREFUND unfixed below the gate), got balance=%s want=0 -- the gate changed already-committed-equivalent behavior it must not touch", account.BalanceZNHB)
			}
		})
	}
}
