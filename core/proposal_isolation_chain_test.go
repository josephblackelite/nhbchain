package core

import (
	"errors"
	"fmt"
	"testing"

	"nhbchain/core/types"
	nativecommon "nhbchain/native/common"
)

// A transaction queued behind another of the same sender is refused when it is
// applied alone against committed state, because the nonce it needs has not
// been reached yet, yet it applies normally in a block that holds its
// predecessor. Isolation must therefore keep such a transaction in the joint
// trials, so that a transaction queued behind one of its own sender is judged
// in the same way as any other.

// isolationChain queues two transfers from one funded sender (nonces 0 and 1)
// and then one transfer from each of others further senders, all injected into
// the mempool in that order.
func isolationChain(t testing.TB, node *Node, others int) (first, second *types.Transaction, rest []*types.Transaction) {
	t.Helper()
	key := livenessKey(t)
	livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000_000, 0)
	first = livenessTransfer(t, key, 0)
	second = livenessTransfer(t, key, 1)
	livenessInject(node, first, second)
	rest = livenessFundedTransfers(t, node, others)
	return first, second, rest
}

// requireNonceAheadSkip checks the premise of these tests: applied alone
// against committed state, tx is refused only because its nonce is ahead, and
// the classifier files that under Skip.
func requireNonceAheadSkip(t testing.TB, node *Node, tx *types.Transaction) {
	t.Helper()
	node.stateMu.Lock()
	sp, err := node.freshBlockStateLocked(node.newBuildCtx(nil))
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("fresh state: %v", err)
	}
	applyErr := sp.ApplyTransaction(tx)
	sp.EndBlock()
	if !errors.Is(applyErr, ErrNonceTooHigh) {
		t.Fatalf("setup: expected ErrNonceTooHigh, got %v", applyErr)
	}
	if got := classifyProposalError(applyErr); got != proposalDispositionSkip {
		t.Fatalf("setup: expected the Skip disposition, got %s", dispositionLabel(got))
	}
}

// failWhenPresent returns a stage hook that fails whenever any of txs is in the
// block being built.
func failWhenPresent(txs ...*types.Transaction) func(*StateProcessor, []*types.Transaction) error {
	return func(sp *StateProcessor, blockTxs []*types.Transaction) error {
		for _, tx := range blockTxs {
			for _, target := range txs {
				if tx == target {
					return errFakeLifecycle
				}
			}
		}
		return nil
	}
}

// emptyFallbacks drives CreateBlock twice over the whole mempool; each build is
// expected to fall back to an empty block, which starts isolation.
func emptyFallbacks(t testing.TB, node *Node) {
	t.Helper()
	for i := 0; i < 2; i++ {
		block, err := node.CreateBlock(node.GetMempool())
		if err != nil {
			t.Fatalf("CreateBlock %d must fall back to an empty block: %v", i, err)
		}
		if len(block.Transactions) != 0 {
			t.Fatalf("CreateBlock %d: expected the empty fallback, got %d transactions", i, len(block.Transactions))
		}
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
	livenessWaitIsolation(t, node)
}

// The transaction that makes a late stage fail is found and evicted even when
// it is the second of two from one sender, and the build then recovers.
func TestIsolationAttributesAFailureBehindASameSenderPredecessor(t *testing.T) {
	for _, stage := range []string{"lifecycle", "evidence"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			node := newTestNode(t)
			first, second, rest := isolationChain(t, node, 3)
			requireNonceAheadSkip(t, node, second)

			if stage == "lifecycle" {
				node.setProposalLifecycleHook(failWhenPresent(second))
			} else {
				node.setProposalEvidenceHook(failWhenPresent(second))
			}
			emptyFallbacks(t, node)

			if livenessResident(t, node, second) {
				t.Fatalf("the transaction that makes the %s stage fail must be evicted", stage)
			}
			if !livenessResident(t, node, first) {
				t.Fatalf("the predecessor is innocent and must stay resident")
			}
			for i, tx := range rest {
				if !livenessResident(t, node, tx) {
					t.Fatalf("isolation evicted an innocent transaction (%d)", i)
				}
			}

			block, err := node.CreateBlock(node.GetMempool())
			if err != nil {
				t.Fatalf("CreateBlock after isolation: %v", err)
			}
			if len(block.Transactions) != 4 {
				t.Fatalf("expected the 4 remaining transactions, got %d", len(block.Transactions))
			}
		})
	}
}

// When the culprit is the predecessor itself, it is evicted and its successor,
// which now waits on a nonce that will not arrive, is left resident: a nonce
// gap is a reason to wait, not a reason to evict.
func TestIsolationKeepsTheSuccessorOfAnEvictedPredecessor(t *testing.T) {
	node := newTestNode(t)
	first, second, rest := isolationChain(t, node, 3)
	node.setProposalLifecycleHook(failWhenPresent(first))
	emptyFallbacks(t, node)

	if livenessResident(t, node, first) {
		t.Fatalf("the transaction that makes the lifecycle fail must be evicted")
	}
	if !livenessResident(t, node, second) {
		t.Fatalf("a transaction that only waits for its predecessor must stay resident")
	}
	for i, tx := range rest {
		if !livenessResident(t, node, tx) {
			t.Fatalf("isolation evicted an innocent transaction (%d)", i)
		}
	}
}

// A second culprit is still found after the first one is evicted from in front
// of a transaction that can then no longer apply.
func TestIsolationFindsASecondCulpritAfterEvictingAPredecessor(t *testing.T) {
	node := newTestNode(t)
	first, second, rest := isolationChain(t, node, 4)
	other := rest[2]
	node.setProposalLifecycleHook(failWhenPresent(first, other))
	emptyFallbacks(t, node)

	if livenessResident(t, node, first) {
		t.Fatalf("the first culprit must be evicted")
	}
	if livenessResident(t, node, other) {
		t.Fatalf("the second culprit must be evicted")
	}
	if !livenessResident(t, node, second) {
		t.Fatalf("the waiting successor must stay resident")
	}
	for i, tx := range rest {
		if tx != other && !livenessResident(t, node, tx) {
			t.Fatalf("isolation evicted an innocent transaction (%d)", i)
		}
	}
}

// A transaction whose nonce is ahead and whose predecessor is not queued at all
// cannot take part in a trial; it stays resident and does not stop the real
// culprit from being found.
func TestIsolationSkipsAnOrphanedNonceAheadTransactionWhileBisecting(t *testing.T) {
	node := newTestNode(t)
	key := livenessKey(t)
	livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000_000, 0)
	orphan := livenessTransfer(t, key, 1)
	livenessInject(node, orphan)
	rest := livenessFundedTransfers(t, node, 5)
	culprit := rest[3]
	requireNonceAheadSkip(t, node, orphan)

	node.setProposalLifecycleHook(failWhenPresent(culprit))
	emptyFallbacks(t, node)

	if livenessResident(t, node, culprit) {
		t.Fatalf("the transaction that makes the lifecycle fail must be evicted")
	}
	if !livenessResident(t, node, orphan) {
		t.Fatalf("the orphaned nonce-ahead transaction must stay resident")
	}
	for i, tx := range rest {
		if tx != culprit && !livenessResident(t, node, tx) {
			t.Fatalf("isolation evicted an innocent transaction (%d)", i)
		}
	}
}

// A transaction that a solo dry run refuses for a transient reason but that
// applies fine alongside the others is still attributable when it is the one
// that makes a late stage fail.
func TestIsolationAttributesATransientlyRefusedTransactionThatAppliesJointly(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 6)
	target := txs[3]
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		// The solo dry run is the only caller that passes a negative wave.
		if tx == target && wave < 0 {
			return fmt.Errorf("guard: %w", nativecommon.ErrModulePaused)
		}
		return nil
	})
	node.setProposalLifecycleHook(failWhenPresent(target))

	node.isolate(append([]*types.Transaction(nil), txs...))

	if livenessResident(t, node, target) {
		t.Fatalf("the transaction that makes the lifecycle fail must be evicted")
	}
	for i, tx := range txs {
		if tx != target && !livenessResident(t, node, tx) {
			t.Fatalf("isolation evicted an innocent transaction (%d)", i)
		}
	}
}
