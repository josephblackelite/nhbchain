package state

import "testing"

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
