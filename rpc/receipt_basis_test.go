package rpc

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"nhbchain/core/types"
)

// A transaction receipt is rebuilt when it is asked for: the node stores neither the
// gas a transaction used nor the events it emitted, so gasUsed and logs come from a
// simulation on the CURRENT state or, when that fails, from the transaction's own
// gas limit and a made-up transfer log. A reader must be told that, field by field,
// instead of finding it out from the code (PL-DC-22). Status is 0x1 for every
// transaction found in a block because a block never contains a transaction that
// failed to apply; that is a property of the chain and is stated, not guessed.
func TestReceiptSaysWhatItDoesNotKnow(t *testing.T) {
	s := &Server{} // no node: the simulation is unavailable, as it is for an old transaction
	tx := &types.Transaction{
		ChainID: big.NewInt(1), Type: types.TxTypeTransfer, Nonce: 1,
		To: make([]byte, 20), Value: big.NewInt(5), GasLimit: 21000, GasPrice: big.NewInt(1),
	}
	receipt, err := s.buildReceiptResult(context.Background(), tx, "0xabc", []byte{1, 2, 3}, 9)
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	raw, _ := json.Marshal(receipt)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["reconstructed"] != true {
		t.Fatalf("a receipt that is rebuilt on request must say reconstructed=true: %s", raw)
	}
	if got["gasUsedKnown"] != false || got["logsKnown"] != false {
		t.Fatalf("gasUsed and logs are not stored, so gasUsedKnown and logsKnown must be false: %s", raw)
	}
	if got["gasUsedBasis"] != "transaction gas limit" {
		t.Fatalf("with no simulation the gas figure is the transaction's own limit and must say so, got %v", got["gasUsedBasis"])
	}
	if got["logsBasis"] != "synthesized from the transaction fields" {
		t.Fatalf("the transfer log is synthesized and must say so, got %v", got["logsBasis"])
	}
	if got["statusBasis"] != "included in a block" {
		t.Fatalf("status 0x1 stands for 'included in a block' and must say so, got %v", got["statusBasis"])
	}
	// The existing fields keep their names and values.
	if got["status"] != "0x1" || got["gasUsed"] != "0x5208" {
		t.Fatalf("existing fields changed: %s", raw)
	}
}
