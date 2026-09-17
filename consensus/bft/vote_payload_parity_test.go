package bft

import (
	"bytes"
	"testing"

	"nhbchain/core/types"
)

// TestVotePayloadMatchesQuorumCertPayload guards against silent drift
// between consensus/bft.Vote (the payload actually signed during a live
// BFT round, via createVote/verifySignedVote) and types.PrecommitVote (an
// independently-defined, structurally-identical type core/node.go's
// QuorumCert verifier uses to reconstruct that same payload -- it cannot
// import consensus/bft to reuse the original type directly without risking
// a package cycle, since consensus/bft already imports core/types).
//
// These are deliberately two separate Go types, not a shared one, so this
// test is the thing standing between "someone changes one and forgets the
// other" and a quorum certificate silently failing to verify (or worse,
// verifying against the wrong bytes) in production. If this test ever
// fails, do not just update the expected bytes -- it means the two structs
// have diverged and every already-issued QuorumCert may no longer verify.
func TestVotePayloadMatchesQuorumCertPayload(t *testing.T) {
	blockHash := []byte{0xAA, 0xBB, 0xCC, 0xDD}

	original := &Vote{BlockHash: blockHash, Round: 3, Type: Precommit, Height: 42}
	mirrored := &types.PrecommitVote{BlockHash: blockHash, Round: 3, Type: types.VoteTypePrecommit, Height: 42}

	originalBytes := original.bytes()
	mirroredBytes := mirrored.Bytes()

	if !bytes.Equal(originalBytes, mirroredBytes) {
		t.Fatalf("SECURITY: consensus/bft.Vote and types.PrecommitVote produced different bytes for the identical logical vote -- a QuorumCert built from real bft.Vote signatures will fail (or worse, silently mis-verify) against core/node.go's types.PrecommitVote-based verifier.\nbft.Vote:          %s\ntypes.PrecommitVote: %s", originalBytes, mirroredBytes)
	}

	// Same check for Prevote, and for a nil/empty block hash (the "vote for
	// NIL" case broadcastPrevoteNilLocked produces) -- both are real shapes
	// this payload takes in production, not just the Precommit/populated
	// case above.
	prevoteOriginal := &Vote{BlockHash: blockHash, Round: 3, Type: Prevote, Height: 42}
	prevoteMirrored := &types.PrecommitVote{BlockHash: blockHash, Round: 3, Type: types.VoteTypePrevote, Height: 42}
	if !bytes.Equal(prevoteOriginal.bytes(), prevoteMirrored.Bytes()) {
		t.Fatalf("SECURITY: Prevote-typed payloads diverged between bft.Vote and types.PrecommitVote")
	}

	nilOriginal := &Vote{BlockHash: nil, Round: 0, Type: Precommit, Height: 1}
	nilMirrored := &types.PrecommitVote{BlockHash: nil, Round: 0, Type: types.VoteTypePrecommit, Height: 1}
	if !bytes.Equal(nilOriginal.bytes(), nilMirrored.Bytes()) {
		t.Fatalf("SECURITY: nil-blockhash payloads diverged between bft.Vote and types.PrecommitVote")
	}

	// And the VoteType byte values themselves must match -- these get
	// JSON-marshaled as numbers, so a mismatched constant value would
	// silently produce different bytes despite both types.go and vote.go
	// naming their constants the same way.
	if byte(Prevote) != byte(types.VoteTypePrevote) {
		t.Fatalf("SECURITY: Prevote constant mismatch: bft=%d types=%d", Prevote, types.VoteTypePrevote)
	}
	if byte(Precommit) != byte(types.VoteTypePrecommit) {
		t.Fatalf("SECURITY: Precommit constant mismatch: bft=%d types=%d", Precommit, types.VoteTypePrecommit)
	}
}
