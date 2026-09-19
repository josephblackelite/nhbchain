package core

import (
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"nhbchain/core/types"
)

// Panic injection: a panic while applying a candidate transaction, or while
// finishing a block (lifecycle, evidence), must never crash the node or leave a
// lock held. The apply/lifecycle/evidence hooks are test seams that run inside
// CreateBlock's proposer-side execution only.

// livenessMetricValue sums the values of the samples of metric name (optionally
// restricted to samples carrying every given label) in the default registry.
func livenessMetricValue(t testing.TB, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var total float64
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			match := true
			for want, value := range labels {
				found := false
				for _, pair := range metric.GetLabel() {
					if pair.GetName() == want && pair.GetValue() == value {
						found = true
					}
				}
				if !found {
					match = false
				}
			}
			if !match {
				continue
			}
			switch {
			case metric.GetCounter() != nil:
				total += metric.GetCounter().GetValue()
			case metric.GetGauge() != nil:
				total += metric.GetGauge().GetValue()
			case metric.GetHistogram() != nil:
				total += float64(metric.GetHistogram().GetSampleCount())
			}
		}
	}
	return total
}

// livenessFundedTransfers returns n valid transfers from n distinct funded
// senders, injected into the mempool.
func livenessFundedTransfers(t testing.TB, node *Node, n int) []*types.Transaction {
	t.Helper()
	out := make([]*types.Transaction, 0, n)
	for i := 0; i < n; i++ {
		key := livenessKey(t)
		livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000_000, 0)
		out = append(out, livenessTransfer(t, key, 0))
	}
	livenessInject(node, out...)
	return out
}

// requireLocksFree fails if the state or mempool lock is still held: a panic
// that unwound through a build must not leave either one locked.
func requireLocksFree(t testing.TB, node *Node) {
	t.Helper()
	livenessWaitIsolation(t, node)
	if !node.stateMu.TryLock() {
		t.Fatalf("stateMu is still held after the build returned")
	}
	node.stateMu.Unlock()
	if !node.mempoolMu.TryLock() {
		t.Fatalf("mempoolMu is still held after the build returned")
	}
	node.mempoolMu.Unlock()
}

func TestPanicInApplyIsContainedForFirstMiddleAndLastTransaction(t *testing.T) {
	for _, position := range []string{"first", "middle", "last"} {
		position := position
		t.Run(position, func(t *testing.T) {
			node := newTestNode(t)
			txs := livenessFundedTransfers(t, node, 5)
			target := map[string]int{"first": 0, "middle": 2, "last": 4}[position]
			victim := txs[target]
			node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
				if tx == victim {
					panic("injected apply panic in " + position)
				}
				return nil
			})

			panicsBefore := livenessPanicsRecovered(node)
			metricBefore := livenessMetricValue(t, "nhb_consensus_tx_panics_recovered_total", nil)
			block, err := node.CreateBlock(node.GetMempool())
			if err != nil {
				t.Fatalf("CreateBlock must contain an apply panic, got error: %v", err)
			}
			if got := len(block.Transactions); got != 4 {
				t.Fatalf("expected the 4 healthy transactions in the block, got %d", got)
			}
			if livenessBlockContains(t, block, victim) {
				t.Fatalf("the panicking transaction must not be in the block")
			}
			if livenessPanicsRecovered(node) <= panicsBefore {
				t.Fatalf("expected the recovered panic to be counted")
			}
			if got := livenessMetricValue(t, "nhb_consensus_tx_panics_recovered_total", nil); got <= metricBefore {
				t.Fatalf("expected nhb_consensus_tx_panics_recovered_total to increase, %v -> %v", metricBefore, got)
			}
			requireLocksFree(t, node)

			// A contained panic is confirmed by a solo dry run at its FIRST
			// strike (it panics alone too) and evicted.
			if livenessResident(t, node, victim) {
				t.Fatalf("the panicking transaction should have been evicted from the mempool at its first strike")
			}
			// The four included transactions stay leased until the block commits
			// (or the round fails and requeues them); the evicted one is gone.
			if n := livenessInFlight(node); n != 4 {
				t.Fatalf("expected exactly the 4 included transactions to stay leased, got %d", n)
			}
			if err := node.ValidateBlock(block); err != nil {
				t.Fatalf("the block must validate: %v", err)
			}

			// With the hook cleared the node builds normally.
			node.setProposalApplyHook(nil)
			if _, err := node.CreateBlock(nil); err != nil {
				t.Fatalf("CreateBlock after the panic: %v", err)
			}
		})
	}
}

func TestPanicInEveryTransactionStillProducesABlock(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 6)
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		panic("injected: every apply panics")
	})

	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock must contain panics from every transaction, got error: %v", err)
	}
	if len(block.Transactions) != 0 {
		t.Fatalf("expected an empty block, got %d transactions", len(block.Transactions))
	}
	requireLocksFree(t, node)
	for i, tx := range txs {
		if livenessResident(t, node, tx) {
			t.Fatalf("transaction %d panicked alone as well and should have been evicted", i)
		}
	}

	node.setProposalApplyHook(nil)
	if _, err := node.CreateBlock(nil); err != nil {
		t.Fatalf("CreateBlock after the panics: %v", err)
	}
}

// A transaction that fails ordinarily in the waves but panics in the solo dry
// run that decides its eviction: the panic inside the confirmation step must be
// contained too, and count as "fails alone".
func TestPanicInSoloConfirmationIsContained(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 3)
	victim := txs[1]
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		if tx != victim {
			return nil
		}
		if wave == -1 {
			panic("injected panic in the solo dry run")
		}
		return errors.New("injected ordinary failure")
	})

	// Two builds strike the victim without confirming (N is 3); the third
	// reaches the strike limit and runs the solo confirmation, which panics.
	for i := 1; i <= 3; i++ {
		block, err := node.CreateBlock(txs)
		if err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
		if got := len(block.Transactions); got != 2 {
			t.Fatalf("build %d: expected the 2 healthy transactions, got %d", i, got)
		}
		if i < 3 && !livenessResident(t, node, victim) {
			t.Fatalf("build %d: the victim was evicted before reaching the strike limit", i)
		}
	}
	if livenessResident(t, node, victim) {
		t.Fatalf("the victim failed alone (it panicked) at its third strike and should have been evicted")
	}
	requireLocksFree(t, node)
}

func TestPanicInLifecycleAndEvidenceStagesIsContained(t *testing.T) {
	for _, stage := range []string{"lifecycle", "evidence"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			node := newTestNode(t)
			txs := livenessFundedTransfers(t, node, 3)

			// The stage panics only while transactions are present: the empty
			// fallback block must still be built.
			boom := func(sp *StateProcessor, blockTxs []*types.Transaction) error {
				if len(blockTxs) > 0 {
					panic("injected panic in the " + stage + " stage")
				}
				return nil
			}
			if stage == "lifecycle" {
				node.setProposalLifecycleHook(boom)
			} else {
				node.setProposalEvidenceHook(boom)
			}

			block, err := node.CreateBlock(node.GetMempool())
			if err != nil {
				t.Fatalf("CreateBlock must fall back to an empty block when the %s stage panics, got: %v", stage, err)
			}
			if len(block.Transactions) != 0 {
				t.Fatalf("expected the empty fallback block, got %d transactions", len(block.Transactions))
			}
			requireLocksFree(t, node)
			for i, tx := range txs {
				if !livenessResident(t, node, tx) {
					t.Fatalf("transaction %d must stay resident: the failure was not its fault", i)
				}
			}
			if n := livenessInFlight(node); n != 0 {
				t.Fatalf("the fallback must release every in-flight lease, %d remain", n)
			}

			// The stage panics for every block, empty or not: nothing can be
			// built, but CreateBlock still returns an error rather than
			// panicking, and the node stays usable once the fault clears.
			always := func(sp *StateProcessor, blockTxs []*types.Transaction) error {
				panic("injected panic in the " + stage + " stage, always")
			}
			if stage == "lifecycle" {
				node.setProposalLifecycleHook(always)
			} else {
				node.setProposalEvidenceHook(always)
			}
			if _, err := node.CreateBlock(node.GetMempool()); err == nil {
				t.Fatalf("expected an error when even the empty block cannot be built")
			}
			requireLocksFree(t, node)

			node.setProposalLifecycleHook(nil)
			node.setProposalEvidenceHook(nil)
			if _, err := node.CreateBlock(nil); err != nil {
				t.Fatalf("CreateBlock after the fault cleared: %v", err)
			}
		})
	}
}
