package state

import (
	"math/big"
	"testing"

	"nhbchain/native/governance"
	"nhbchain/native/potso"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func TestGovernanceNamespaces(t *testing.T) {
	propKey := GovernanceProposalKey(42)
	if string(propKey) != "gov/proposals/42" {
		t.Fatalf("unexpected proposal key: %s", string(propKey))
	}

	voteKey := GovernanceVoteKey(42, []byte{0x01, 0x02, 0x03})
	if string(voteKey) != "gov/votes/42/010203" {
		t.Fatalf("unexpected vote key: %s", string(voteKey))
	}

	seqKey := GovernanceSequenceKey()
	if string(seqKey) != "gov/seq" {
		t.Fatalf("unexpected sequence key: %s", string(seqKey))
	}

	escrowKey := GovernanceEscrowKey([]byte{0xaa, 0xbb})
	expectedEscrow := append([]byte("gov/escrow/"), 0xaa, 0xbb)
	if string(escrowKey) != string(expectedEscrow) {
		t.Fatalf("unexpected escrow key: %v", escrowKey)
	}

	paramKey := ParamStoreKey("fees.baseFee")
	if string(paramKey) != "params/fees.baseFee" {
		t.Fatalf("unexpected param key: %s", string(paramKey))
	}

	snapshotKey := SnapshotPotsoWeightsKey(99)
	if string(snapshotKey) != "snapshots/potso/99/weights" {
		t.Fatalf("unexpected snapshot key: %s", string(snapshotKey))
	}
}

func TestParamStoreSetGet(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	trie, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	manager := NewManager(trie)

	if err := manager.ParamStoreSet("fees.baseFee", []byte("25")); err != nil {
		t.Fatalf("param set: %v", err)
	}

	value, ok, err := manager.ParamStoreGet("fees.baseFee")
	if err != nil {
		t.Fatalf("param get: %v", err)
	}
	if !ok {
		t.Fatalf("expected parameter present")
	}
	if string(value) != "25" {
		t.Fatalf("unexpected parameter value: %s", string(value))
	}

	if _, _, err := manager.ParamStoreGet("  "); err == nil {
		t.Fatalf("expected error for empty key")
	}
}

func TestMinimumValidatorStakePersistence(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	trie, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	manager := NewManager(trie)

	value, err := manager.MinimumValidatorStake()
	if err != nil {
		t.Fatalf("minimum stake default: %v", err)
	}
	if value.Cmp(governance.DefaultMinimumValidatorStake()) != 0 {
		t.Fatalf("unexpected default minimum stake: %s", value.String())
	}

	if err := manager.SetMinimumValidatorStake(big.NewInt(4500)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	stored, err := manager.MinimumValidatorStake()
	if err != nil {
		t.Fatalf("minimum stake stored: %v", err)
	}
	if stored.Cmp(big.NewInt(4500)) != 0 {
		t.Fatalf("unexpected stored minimum stake: %s", stored.String())
	}

	if err := manager.SetMinimumValidatorStake(big.NewInt(0)); err == nil {
		t.Fatalf("expected error when setting non-positive minimum stake")
	}
}

// TestSnapshotPotsoWeightsRoundTrip covers the governance-facing weight
// snapshot key on its own, at the Manager level -- previously only the key
// *format* (SnapshotPotsoWeightsKey) had any test coverage at all; nothing
// ever exercised an actual write, which is exactly how the "governance:
// potso snapshot unavailable" bug went unnoticed (see core/state_transition.go's
// processPotsoRewardEpoch and core/potso_rewards_integration_test.go for the
// production write path and its own coverage of this same key).
func TestSnapshotPotsoWeightsRoundTrip(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	trie, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	manager := NewManager(trie)

	if _, ok, err := manager.SnapshotPotsoWeights(7); err != nil {
		t.Fatalf("unexpected error for missing snapshot: %v", err)
	} else if ok {
		t.Fatalf("expected no snapshot before any write")
	}

	if err := manager.SetSnapshotPotsoWeights(7, nil); err == nil {
		t.Fatalf("expected error when persisting a nil snapshot")
	}

	addr := [20]byte{9}
	snapshot := &potso.StoredWeightSnapshot{
		Epoch:           7,
		TotalStake:      big.NewInt(1000),
		TotalEngagement: 42,
		Entries: []potso.StoredWeightEntry{
			{Address: addr, Stake: big.NewInt(1000), Engagement: 42, WeightBps: 10000},
		},
	}
	if err := manager.SetSnapshotPotsoWeights(7, snapshot); err != nil {
		t.Fatalf("set snapshot: %v", err)
	}

	stored, ok, err := manager.SnapshotPotsoWeights(7)
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if !ok || stored == nil {
		t.Fatalf("expected snapshot to be present after write")
	}
	if stored.Epoch != 7 || len(stored.Entries) != 1 || stored.Entries[0].Address != addr {
		t.Fatalf("unexpected stored snapshot: %+v", stored)
	}
	if stored.Entries[0].WeightBps != 10000 {
		t.Fatalf("unexpected weight bps: %d", stored.Entries[0].WeightBps)
	}

	// Writing under a different epoch must not disturb epoch 7's record.
	if _, ok, err := manager.SnapshotPotsoWeights(8); err != nil {
		t.Fatalf("unexpected error for adjacent epoch: %v", err)
	} else if ok {
		t.Fatalf("expected epoch 8 to remain unwritten")
	}
}
