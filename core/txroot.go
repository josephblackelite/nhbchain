package core

import (
	"nhbchain/core/txroot"
	"nhbchain/core/types"
)

// ComputeTxRoot builds the canonical transaction trie for the provided
// transactions and returns its root hash. The implementation lives in
// core/txroot, where consensus/bft can reach it too (see txroot.Compute).
func ComputeTxRoot(txs []*types.Transaction) ([]byte, error) {
	return txroot.Compute(txs)
}
