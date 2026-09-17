package core

import (
	"encoding/json"
	"testing"

	"nhbchain/core/types"
)

// TestApplyTransactionRejectsNilInsteadOfPanicking is an independent
// regression test for an externally-reported finding (bug bounty submission
// NHB-TRIAGE-H20, not formally submitted for a bounty but present in the
// same triage test file as NHB-TRIAGE-C4/C7): types.Block.Transactions is
// []*Transaction, so a P2P block-gossip JSON payload containing
// "transactions":[null] decodes to a nil pointer in that slice with no
// decode error. The block-level nil checks in the P2P block-sync path
// (handleNetworkBlocks) only look at the block and its header, never at
// individual transactions, so a nil entry reaches commitBlock's apply loop,
// which calls StateProcessor.ApplyTransaction directly on it.
// executeTransaction's first line used to dereference tx.ChainID
// unconditionally -- a nil-pointer panic reachable from a single,
// unauthenticated, crafted P2P message, and (like NHB-TRIAGE-C2) nothing in
// the P2P message-receive path recovers a panic, so it crashed the entire
// process, not just that one message.
//
// This test decodes the exact JSON shape a real gossiped block would carry
// (mirroring how the block-sync path unmarshals p2p.BlocksPayload) rather
// than constructing a nil *types.Transaction by hand, to prove the decode
// step itself, not just a synthetic nil, produces the dangerous value.
func TestApplyTransactionRejectsNilInsteadOfPanicking(t *testing.T) {
	var block types.Block
	if err := json.Unmarshal([]byte(`{"header":{"height":9},"transactions":[null]}`), &block); err != nil {
		t.Fatalf("decode block: %v", err)
	}
	if len(block.Transactions) != 1 || block.Transactions[0] != nil {
		t.Fatalf("expected a single nil transaction element from decoding, got %#v", block.Transactions)
	}

	sp, _ := newTestStateProcessor(t)

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("SECURITY: ApplyTransaction(nil) panicked instead of returning an error: %v", r)
			}
		}()
		if err := sp.ApplyTransaction(block.Transactions[0]); err == nil {
			t.Fatalf("expected ApplyTransaction to reject a nil transaction with an error")
		}
	}()
}
