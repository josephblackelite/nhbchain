package core

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"nhbchain/p2p"
)

// PL-R5T-03: a transaction refused only because it exceeds THIS node's own size
// limit (derived from its frame configuration) is not a protocol violation by
// the peer that relayed it: a node with a different limit accepts it. Before
// this, it was reported to the p2p layer as an invalid payload, which scores the
// relaying peer down and disconnects it. With two validators on the same build
// the case cannot arise, but during a rolling upgrade it would have cost the
// only link. A transaction that is invalid for every node still is a violation,
// and the RPC path still reports an oversized transaction as invalid.
func TestOversizedGossipedTransactionDoesNotPenaliseThePeerThatRelayedIt(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)
	key := livenessKey(t)
	livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000_000_000, 0)

	huge := signedTransferWithData(t, key, 0, 1, 700<<10)
	payload, err := json.Marshal(huge)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	err = node.ProcessNetworkMessage(&p2p.Message{Type: p2p.MsgTypeTx, Payload: payload})
	if p2p.IsInvalidPayload(err) {
		t.Fatalf("a transaction over this node's own size limit was reported as an invalid payload (the relaying peer would be penalised): %v", err)
	}
	if err != nil {
		t.Fatalf("expected the oversized gossiped transaction to be dropped quietly, got %v", err)
	}
	if livenessResident(t, node, huge) {
		t.Fatalf("an oversized gossiped transaction was admitted to the mempool")
	}

	// The same transaction through the admission path used by RPC is still an
	// invalid transaction with the byte-limit message.
	if err := node.AddTransaction(huge); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("AddTransaction must still refuse it as invalid, got %v", err)
	}
}

func TestInvalidGossipedTransactionStillPenalisesThePeerThatRelayedIt(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)
	key := livenessKey(t)
	livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000_000_000, 0)

	bad := signedTransferWithData(t, key, 0, 1, 0)
	bad.ChainID = new(big.Int).Add(bad.ChainID, big.NewInt(1))
	payload, err := json.Marshal(bad)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	err = node.ProcessNetworkMessage(&p2p.Message{Type: p2p.MsgTypeTx, Payload: payload})
	if !p2p.IsInvalidPayload(err) {
		t.Fatalf("a transaction with a wrong chain id must still be reported as an invalid payload, got %v", err)
	}
}
