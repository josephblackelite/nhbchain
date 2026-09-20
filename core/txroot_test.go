package core

import (
	"bytes"
	"testing"

	"nhbchain/core/txroot"
	"nhbchain/core/types"
)

// ComputeTxRoot is the root every header on the chain carries; it is the one
// implementation in core/txroot, which consensus/bft checks a block's body against.
func TestComputeTxRootIsTheRootOfTheSharedImplementation(t *testing.T) {
	for _, txs := range [][]*types.Transaction{nil, {{Nonce: 1}}, {{Nonce: 1}, {Nonce: 2}, {Nonce: 3}}} {
		got, err := ComputeTxRoot(txs)
		if err != nil {
			t.Fatalf("compute: %v", err)
		}
		want, err := txroot.Compute(txs)
		if err != nil {
			t.Fatalf("compute (shared): %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("ComputeTxRoot of %d transactions is %x, the shared implementation gives %x", len(txs), got, want)
		}
	}
}
