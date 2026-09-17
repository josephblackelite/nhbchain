package core

import "testing"

// TestNodeRewardHistoryForAddress exercises the read-only address-scoped
// reward history query backing the new nhb_getRewardHistory RPC method. It
// asserts that RewardHistoryForAddress surfaces exactly the already-durable,
// already-deterministic epoch-emission payouts recorded by
// settleEpochRewards for the queried address, and nothing for an address
// that never received a payout.
func TestNodeRewardHistoryForAddress(t *testing.T) {
	sp := newRewardTestState(t)
	addrA := seedEligibleValidator(t, sp, 6000, 10)
	addrB := seedEligibleValidator(t, sp, 4000, 5)

	finalizeRewardEpoch(t, sp)

	node := &Node{state: sp}

	entriesA := node.RewardHistoryForAddress(addrA)
	if len(entriesA) != 1 {
		t.Fatalf("expected 1 history entry for address A, got %d", len(entriesA))
	}
	if entriesA[0].Epoch != 1 {
		t.Fatalf("expected epoch 1, got %d", entriesA[0].Epoch)
	}
	if entriesA[0].Total == nil || entriesA[0].Total.Sign() <= 0 {
		t.Fatalf("expected positive payout total, got %v", entriesA[0].Total)
	}

	entriesB := node.RewardHistoryForAddress(addrB)
	if len(entriesB) != 1 {
		t.Fatalf("expected 1 history entry for address B, got %d", len(entriesB))
	}

	unrelated := make([]byte, 20)
	unrelated[19] = 0xff
	if got := node.RewardHistoryForAddress(unrelated); len(got) != 0 {
		t.Fatalf("expected no history entries for an address with no payouts, got %d", len(got))
	}

	if got := node.RewardHistoryForAddress(nil); got != nil {
		t.Fatalf("expected nil result for empty address, got %v", got)
	}
}
