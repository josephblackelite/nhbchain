package core

import (
	"testing"
	"time"

	"nhbchain/core/types"
)

// Helpers that reach into the proposer-local strike book. They live in their
// own file so the "before the change" demonstration (the new tests run against
// an unmodified tree with throwaway stubs) only has to stub this one file.

// livenessStrikes returns the strike record for tx, if any.
func livenessStrikes(t testing.TB, node *Node, tx *types.Transaction) (txStrikeRecord, bool) {
	t.Helper()
	key, err := transactionKey(tx)
	if err != nil {
		t.Fatalf("key of transaction: %v", err)
	}
	return node.poison.peek(key)
}

// livenessStrikeRecords is the number of resident failure histories.
func livenessStrikeRecords(node *Node) int {
	return node.poison.size()
}

// livenessResetStrikes replaces the node's strike book with an empty one, so a
// row of a table-driven test starts clean.
func livenessResetStrikes(node *Node) {
	node.poison = newTxStrikeBook(defaultStrikeBookCapacity)
}

// livenessPanicsRecovered is the number of apply panics contained so far.
func livenessPanicsRecovered(node *Node) uint64 {
	return node.build.panicsRecovered.Load()
}

// livenessWaitIsolation blocks until any asynchronous isolation run started by
// repeated whole-build failures has finished, so assertions about locks and the
// mempool are not racing it.
func livenessWaitIsolation(t testing.TB, node *Node) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for node.isolating.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("isolation did not finish within 10s")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
