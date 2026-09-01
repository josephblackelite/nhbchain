package core

import (
	"encoding/json"
	"testing"

	"nhbchain/core/types"
	"nhbchain/p2p"
)

// TestUnauthenticatedPeerBlockBypassesValidatorCheck is an independent
// verification of an externally-reported finding (bug bounty submission
// NHB-TRIAGE-C1, not formally submitted for a bounty but present in the
// same triage test file as NHB-TRIAGE-C4/C7): that a block arriving via the
// P2P block-sync path (MsgTypeBlocks/MsgTypeBlock, handled by
// Node.handleNetworkBlocks -> commitSyncedBlock -> commitBlock) is accepted
// and committed with NO check that Header.Validator belongs to the actual
// validator set, and no signature or BFT quorum verification of any kind --
// unlike the legitimate consensus path (MsgTypeProposal/MsgTypeVote,
// handled by the bft.Engine), which does verify both.
//
// This test constructs a block using the node's own CreateBlock (so it is
// structurally self-consistent: correct height, PrevHash, TxRoot, and a
// StateRoot the node will independently re-derive and match, since nothing
// about the block's transactions or resulting state changes), then tampers
// ONLY the Header.Validator field to an address with no relationship to the
// node's real validator set, and feeds it back in exactly as if it had
// arrived from an untrusted network peer.
func TestUnauthenticatedPeerBlockBypassesValidatorCheck(t *testing.T) {
	target := newTestNode(t)
	target.SetNetworkBroadcaster(&testBroadcaster{})

	heightBefore := target.GetHeight()

	block, err := target.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if block == nil || block.Header == nil {
		t.Fatalf("create block: nil block/header")
	}

	// An address that is provably not the node's own real validator key
	// (the only key CreateBlock legitimately stamps into the header) and
	// not registered as a validator anywhere in this fresh node's genesis
	// state -- an arbitrary, unrelated attacker-controlled address.
	attacker := make([]byte, 20)
	for i := range attacker {
		attacker[i] = 0xEE
	}
	block.Header.Validator = attacker

	payload, err := json.Marshal(p2p.BlocksPayload{Blocks: []*types.Block{block}})
	if err != nil {
		t.Fatalf("marshal blocks payload: %v", err)
	}

	// Exactly the code path a raw, unauthenticated TCP peer's gossip
	// message takes -- no signature over this payload is checked anywhere
	// before it reaches handleNetworkBlocks.
	procErr := target.ProcessNetworkMessage(&p2p.Message{Type: p2p.MsgTypeBlocks, Payload: payload})

	heightAfter := target.GetHeight()

	if procErr == nil && heightAfter == heightBefore+1 {
		t.Fatalf("SECURITY: a peer-supplied block with an attacker-controlled Header.Validator (not a member of the real validator set) was committed with no signature or quorum check -- height %d -> %d, ProcessNetworkMessage error = %v", heightBefore, heightAfter, procErr)
	}
	t.Logf("not reproduced against current code: ProcessNetworkMessage returned %v, height %d -> %d", procErr, heightBefore, heightAfter)
}
