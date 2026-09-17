package core

import (
	"testing"
)

// TestPosIntentTxHashFindsRecentEntry confirms the basic cache path: a
// finality update with both a TxHash and an IntentRef becomes lookup-able
// via PosIntentTxHash immediately.
func TestPosIntentTxHashFindsRecentEntry(t *testing.T) {
	node := newTestNode(t)
	intentRef := []byte{0xAA, 0xBB, 0xCC}
	txHash := []byte{0x01, 0x02, 0x03, 0x04}

	node.publishPOSFinality(POSFinalityUpdate{
		IntentRef: intentRef,
		TxHash:    txHash,
		Status:    POSFinalityStatusFinalized,
	})

	got, ok := node.PosIntentTxHash(intentRef)
	if !ok {
		t.Fatalf("expected a cache hit for a just-published intent")
	}
	if string(got) != string(txHash) {
		t.Fatalf("got tx hash %x, want %x", got, txHash)
	}
}

// TestPosIntentTxHashMissReturnsFalseNotError confirms an unknown intent
// is a clean miss, matching the RPC layer's omitempty (never an error)
// contract.
func TestPosIntentTxHashMissReturnsFalseNotError(t *testing.T) {
	node := newTestNode(t)
	got, ok := node.PosIntentTxHash([]byte{0xDE, 0xAD})
	if ok {
		t.Fatalf("expected a cache miss, got hash %x", got)
	}
}

// TestPosIntentTxHashSurvivesHistorySlicePruning is the actual regression
// this cache's own ordered-key design exists to prevent: posStreamHistory
// is capped by TOTAL ENTRY COUNT (each intent contributes up to 2 entries
// -- pending, then finalized), while posIntentTxHash/posIntentTxHashOrder
// are capped by DISTINCT INTENT count. Publish more DISTINCT intents than
// fit in posStreamHistory once each contributes 2 entries (so the
// earliest intents' history slots are pruned from posStreamHistory), but
// far fewer distinct intents than posFinalityHistoryLimit itself (so the
// cache's own eviction never triggers) -- the very first intent's cache
// entry must still resolve even though its own history slots are long
// gone.
func TestPosIntentTxHashSurvivesHistorySlicePruning(t *testing.T) {
	node := newTestNode(t)
	firstIntent := []byte{0x01}
	firstTxHash := []byte{0xF0, 0xF1}

	node.publishPOSFinality(POSFinalityUpdate{
		IntentRef: firstIntent,
		TxHash:    firstTxHash,
		Status:    POSFinalityStatusPending,
	})
	node.publishPOSFinality(POSFinalityUpdate{
		IntentRef: firstIntent,
		TxHash:    firstTxHash,
		Status:    POSFinalityStatusFinalized,
	})

	// Publish enough OTHER distinct intents, each with 2 entries, that the
	// TOTAL entry count comfortably exceeds posFinalityHistoryLimit (so
	// firstIntent's own 2 history slots are pruned from posStreamHistory),
	// while the DISTINCT intent count stays well under
	// posFinalityHistoryLimit (so posIntentTxHashOrder never evicts).
	const otherIntents = posFinalityHistoryLimit/2 + 200
	for i := 0; i < otherIntents; i++ {
		ref := []byte{0x02, byte(i), byte(i >> 8)}
		node.publishPOSFinality(POSFinalityUpdate{IntentRef: ref, TxHash: []byte{0x03}, Status: POSFinalityStatusPending})
		node.publishPOSFinality(POSFinalityUpdate{IntentRef: ref, TxHash: []byte{0x03}, Status: POSFinalityStatusFinalized})
	}

	if got, ok := node.PosIntentTxHash(firstIntent); !ok || string(got) != string(firstTxHash) {
		t.Fatalf("expected firstIntent to still resolve from the distinct-key cache even though its own history slots were pruned, ok=%v got=%x", ok, got)
	}
}

// TestPosIntentTxHashCacheEvictsOldestBeyondCap confirms the cache never
// grows unbounded -- once more than posFinalityHistoryLimit distinct
// intents have been seen, the oldest are evicted.
func TestPosIntentTxHashCacheEvictsOldestBeyondCap(t *testing.T) {
	node := newTestNode(t)
	first := []byte{0x99, 0x99}

	node.publishPOSFinality(POSFinalityUpdate{
		IntentRef: first,
		TxHash:    []byte{0x01},
		Status:    POSFinalityStatusFinalized,
	})

	for i := 0; i < posFinalityHistoryLimit+5; i++ {
		node.publishPOSFinality(POSFinalityUpdate{
			IntentRef: []byte{0x50, byte(i), byte(i >> 8)},
			TxHash:    []byte{0x02},
			Status:    POSFinalityStatusFinalized,
		})
	}

	if _, ok := node.PosIntentTxHash(first); ok {
		t.Fatalf("expected the very first intent to have been evicted after exceeding the cache cap")
	}
	if len(node.posIntentTxHash) > posFinalityHistoryLimit {
		t.Fatalf("cache size %d exceeds posFinalityHistoryLimit %d", len(node.posIntentTxHash), posFinalityHistoryLimit)
	}
}
