package state

import (
	"testing"

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
