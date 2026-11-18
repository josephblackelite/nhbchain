package core

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"nhbchain/config"
	"nhbchain/consensus/potso/evidence"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	nativecommon "nhbchain/native/common"
	nativeparams "nhbchain/native/params"
)

// Isolation runs on a node's own mempool after repeated whole-build failures.
// These tests pin down what it may evict: a transaction refused only for a
// designed-transient reason (a pause, a rolling cap, a role that can still be
// granted) stays resident exactly as it does when a wave meets it, a failure
// that cannot pass is still evicted, and a failure of the evidence stage is
// attributed to its transaction the way a lifecycle failure is.

// A transaction refused only because its module is paused stays in the mempool
// through an isolation pass, while the transaction that really makes the block
// fail is evicted and the innocent ones are untouched.
func TestIsolationKeepsAGovernancePausedTransactionResident(t *testing.T) {
	node := newTestNode(t)

	// Persist the pause so every fresh state copy sees it.
	node.stateMu.Lock()
	store := nativeparams.NewStore(nhbstate.NewManager(node.state.Trie))
	pauseErr := store.SetPauses(config.Pauses{TransferZNHB: true})
	node.stateMu.Unlock()
	if pauseErr != nil {
		t.Fatalf("persist governance pause: %v", pauseErr)
	}

	txs := livenessFundedTransfers(t, node, 4)
	culprit := txs[2]

	senderKey := livenessKey(t)
	livenessFund(t, node, senderKey.PubKey().Address().Bytes(), 1_000_000_000, 1_000_000_000)
	to := make([]byte, 20)
	to[19] = 0x42
	paused := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       to,
		Value:    big.NewInt(1),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := paused.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	livenessInject(node, paused)

	// The premise: this transaction is refused for the pause alone, and the
	// classifier files that under Skip.
	node.stateMu.Lock()
	sp, err := node.freshBlockStateLocked(node.newBuildCtx(nil))
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("fresh state: %v", err)
	}
	applyErr := sp.ApplyTransaction(paused)
	sp.EndBlock()
	if !errors.Is(applyErr, ErrTransferZNHBPaused) {
		t.Fatalf("setup: expected ErrTransferZNHBPaused, got %v", applyErr)
	}
	if got := classifyProposalError(applyErr); got != proposalDispositionSkip {
		t.Fatalf("setup: expected the Skip disposition, got %s", dispositionLabel(got))
	}

	// A lifecycle failure that appears only with the culprit in the block.
	node.setProposalLifecycleHook(func(sp *StateProcessor, blockTxs []*types.Transaction) error {
		for _, tx := range blockTxs {
			if tx == culprit {
				return errFakeLifecycle
			}
		}
		return nil
	})
	for i := 0; i < 2; i++ {
		block, err := node.CreateBlock(node.GetMempool())
		if err != nil {
			t.Fatalf("CreateBlock %d must fall back to an empty block: %v", i, err)
		}
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
	livenessWaitIsolation(t, node)

	if !livenessResident(t, node, paused) {
		t.Fatalf("isolation evicted a transaction that is only waiting for a pause to lift")
	}
	if rec, ok := livenessStrikes(t, node, paused); ok && rec.strikes != 0 {
		t.Fatalf("a paused transaction must carry no strike, got %+v", rec)
	}
	if livenessResident(t, node, culprit) {
		t.Fatalf("isolation must still evict the transaction that makes the lifecycle fail")
	}
	for i, tx := range txs {
		if tx != culprit && !livenessResident(t, node, tx) {
			t.Fatalf("isolation evicted an innocent transaction (%d)", i)
		}
	}
}

// An apply failure on a transaction applied alone decides what isolation does
// with it, by the same classification a wave uses.
func TestIsolationEvictionFollowsTheDisposition(t *testing.T) {
	cases := []struct {
		name         string
		err          error // returned by the apply hook for the target; nil with panics
		panics       bool
		wantResident bool
	}{
		// Skip: a designed-transient cause, so the transaction stays.
		{"module paused", fmt.Errorf("guard: %w", nativecommon.ErrModulePaused), false, true},
		{"rolling cap exceeded", fmt.Errorf("mint: %w", ErrSwapMonthlyCapExceeded), false, true},
		{"request quota exceeded", fmt.Errorf("quota: %w", nativecommon.ErrQuotaRequestsExceeded), false, true},
		{"role can still be granted", ErrSwapAdminUnauthorized, false, true},
		{"nonce ahead of the account", ErrNonceTooHigh, false, true},
		{"report cites a height not reached", &evidence.ValidationError{Reason: evidence.RejectReasonFutureHeight, Message: "height not reached"}, false, true},
		{"reporter quota reached", fmt.Errorf("evidence: %w", evidence.ErrReporterQuota), false, true},
		{"treasury pool short", ErrTreasuryRewardPoolInsufficient, false, true},

		// Anything that cannot be expected to pass is still evicted.
		{"nonce already used", ErrNonceTooLow, false, false},
		{"permanently dead payload", ErrEvidenceInvalidPayload, false, false},
		{"unclassified error", errors.New("an error nobody classified"), false, false},
		{"contained panic", nil, true, false},

		// An infrastructure error says nothing about the transaction.
		{"infrastructure error", context.DeadlineExceeded, false, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			node := newTestNode(t)
			txs := livenessFundedTransfers(t, node, 4)
			target := txs[1]
			node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
				if tx != target {
					return nil
				}
				if tc.panics {
					panic("injected apply panic")
				}
				return tc.err
			})

			node.isolate(append([]*types.Transaction(nil), txs...))

			if got := livenessResident(t, node, target); got != tc.wantResident {
				t.Fatalf("resident after isolation = %v, want %v", got, tc.wantResident)
			}
			if rec, ok := livenessStrikes(t, node, target); ok && tc.wantResident && rec.strikes != 0 {
				t.Fatalf("isolation must not strike a transaction it keeps, got %+v", rec)
			}
			for i, tx := range txs {
				if tx != target && !livenessResident(t, node, tx) {
					t.Fatalf("isolation evicted an innocent transaction (%d)", i)
				}
			}
		})
	}
}

// A transaction that cannot apply even in company (here a paused module) is only
// absent from the joint trial set. It must not stop the others from being
// examined: the one that makes the lifecycle stage fail is still found, and the
// skipped one stays.
func TestIsolationStillBisectsWhileASkipClassTransactionIsPresent(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 6)
	skipped := txs[0]
	culprit := txs[4]
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		if tx == skipped {
			return fmt.Errorf("guard: %w", nativecommon.ErrModulePaused)
		}
		return nil
	})
	node.setProposalLifecycleHook(func(sp *StateProcessor, blockTxs []*types.Transaction) error {
		for _, tx := range blockTxs {
			if tx == culprit {
				return errFakeLifecycle
			}
		}
		return nil
	})

	node.isolate(append([]*types.Transaction(nil), txs...))

	if livenessResident(t, node, culprit) {
		t.Fatalf("the transaction that makes the lifecycle fail must be evicted")
	}
	if !livenessResident(t, node, skipped) {
		t.Fatalf("the skip-class transaction must stay resident")
	}
	for i, tx := range txs {
		if tx != culprit && !livenessResident(t, node, tx) {
			t.Fatalf("isolation evicted an innocent transaction (%d)", i)
		}
	}
}

// A failure of the evidence stage that appears only with one transaction in the
// block is bisected out by isolation, as a lifecycle failure is (see
// TestConflictLifecycleFailureIsolatesTheCulprit).
func TestIsolationAttributesAnEvidenceStageFailure(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 8)
	culprit := txs[5]
	node.setProposalEvidenceHook(func(sp *StateProcessor, blockTxs []*types.Transaction) error {
		for _, tx := range blockTxs {
			if tx == culprit {
				return errors.New("injected evidence-stage failure")
			}
		}
		return nil
	})

	for i := 0; i < 2; i++ {
		block, err := node.CreateBlock(node.GetMempool())
		if err != nil {
			t.Fatalf("CreateBlock %d must fall back to an empty block: %v", i, err)
		}
		if len(block.Transactions) != 0 {
			t.Fatalf("expected the empty fallback, got %d transactions", len(block.Transactions))
		}
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
	livenessWaitIsolation(t, node)

	if livenessResident(t, node, culprit) {
		t.Fatalf("isolation must evict the transaction that makes the evidence stage fail")
	}
	for i, tx := range txs {
		if tx != culprit && !livenessResident(t, node, tx) {
			t.Fatalf("isolation evicted an innocent transaction (%d)", i)
		}
	}

	// With the culprit gone the rest build into one block again.
	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock after isolation: %v", err)
	}
	if len(block.Transactions) != 7 {
		t.Fatalf("expected the 7 remaining transactions, got %d", len(block.Transactions))
	}
}

// The confirmation step that follows repeated strikes holds a transaction to the
// same classification: one that fails alone only for a designed-transient reason
// is kept with its strikes cleared, one that cannot pass is evicted.
func TestConfirmAndEvictFollowsTheSoloDisposition(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantResident bool
		wantSkips    int
	}{
		{"refused alone by a pause", fmt.Errorf("guard: %w", nativecommon.ErrModulePaused), true, 1},
		{"refused alone by a rolling cap", fmt.Errorf("mint: %w", ErrSwapMonthlyCapExceeded), true, 1},
		{"passes alone", nil, true, 1},
		{"unclassified failure alone", errors.New("an error nobody classified"), false, 0},
		{"permanent failure alone", ErrNonceTooLow, false, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			node := newTestNode(t)
			txs := livenessFundedTransfers(t, node, 2)
			target := txs[0]
			node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
				if tx == target {
					return tc.err
				}
				return nil
			})

			ctx := node.newBuildCtx(nil)
			key := ctx.keyOf(target)
			for i := 0; i < ctx.cfg.Strikes; i++ {
				node.poison.noteStrike(key, ctx.height, ctx.now(), "failed in a wave")
			}

			node.confirmAndEvict(ctx, txFailure{Tx: target, Err: errors.New("failed in a wave"), Disposition: proposalDispositionQuarantine})

			if got := livenessResident(t, node, target); got != tc.wantResident {
				t.Fatalf("resident after confirmation = %v, want %v", got, tc.wantResident)
			}
			if tc.wantResident {
				rec, ok := livenessStrikes(t, node, target)
				if !ok {
					t.Fatalf("a kept transaction must keep its history")
				}
				if rec.strikes != 0 {
					t.Fatalf("strikes must be cleared for a transaction that is kept, got %d", rec.strikes)
				}
				if int(rec.skips) != tc.wantSkips {
					t.Fatalf("expected %d skip noted, got %d", tc.wantSkips, rec.skips)
				}
			}
			if !livenessResident(t, node, txs[1]) {
				t.Fatalf("an unrelated transaction must stay resident")
			}
		})
	}
}
