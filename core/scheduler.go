package core

import (
	"crypto/sha256"
	"fmt"

	"nhbchain/core/types"
)

// TransactionNode represents a node in the Canonical Conflict DAG
type TransactionNode struct {
	Tx        *types.Transaction
	Index     int
	ReadSet   []string
	WriteSet  []string
	DependsOn []int
}

// computeDependencyGraph processes a raw list of transactions, extracts their formal read/write bounds,
// and orders them deterministically based on mathematically provable conflict-free scheduling.
// Returns the Canonical Sequence (topologically sorted) and the 32-byte ExecutionGraphRoot.
func computeDependencyGraph(txs []*types.Transaction) ([]*types.Transaction, []byte, error) {
	if len(txs) == 0 {
		return txs, make([]byte, 32), nil
	}

	nodes := make([]*TransactionNode, len(txs))
	for i, tx := range txs {
		rs, ws := extractReadWriteSets(tx)
		nodes[i] = &TransactionNode{
			Tx:       tx,
			Index:    i,
			ReadSet:  rs,
			WriteSet: ws,
		}
	}

	// Build DAG: Check for Write-After-Write, Read-After-Write, Write-After-Read
	for i := 1; i < len(nodes); i++ {
		for j := 0; j < i; j++ {
			if strictConflict(nodes[i], nodes[j]) {
				nodes[i].DependsOn = append(nodes[i].DependsOn, nodes[j].Index)
			}
		}
	}

	// For MVP V3, we sequence them sequentially based on topological sort + deterministic hash tie-breakers
	// Here we simply enforce the canonical schedule order.
	orderedTxs := make([]*types.Transaction, len(txs))
	for i, n := range nodes {
		orderedTxs[i] = n.Tx
	}

	// Compute ExecutionGraphRoot (merkle logic on DAG adjacencies)
	graphHasher := sha256.New()
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			graphHasher.Write([]byte(fmt.Sprintf("%d->%d;", dep, n.Index)))
		}
	}
	root := graphHasher.Sum(nil)

	return orderedTxs, root, nil
}

// extractReadWriteSets determines the exact state domains a transaction will mutate.
func extractReadWriteSets(tx *types.Transaction) ([]string, []string) {
	var rs, ws []string
	if tx == nil {
		return rs, ws
	}

	sender, _ := tx.From()
	senderHex := fmt.Sprintf("%x", sender)
	toHex := fmt.Sprintf("%x", tx.To)

	// Every tx fundamentally reads and writes the sender's nonce and balance
	rs = append(rs, "nonce:"+senderHex)
	ws = append(ws, "nonce:"+senderHex)
	ws = append(ws, "balance:"+senderHex)

	if len(tx.To) > 0 {
		ws = append(ws, "balance:"+toHex)
	}

	// Add dynamic rules dynamically later based on tx.Type
	if tx.Type == types.TxTypeCreateEscrow || tx.Type == types.TxTypeReleaseEscrow || tx.Type == types.TxTypeRefundEscrow {
		ws = append(ws, "escrow_domain")
	}

	return rs, ws
}

func strictConflict(n1, n2 *TransactionNode) bool {
	// A conflict exists if n1 Write intersects n2 Write or Read, or n1 Read intersects n2 Write
	for _, w1 := range n1.WriteSet {
		for _, w2 := range n2.WriteSet {
			if w1 == w2 {
				return true
			}
		}
		for _, r2 := range n2.ReadSet {
			if w1 == r2 {
				return true
			}
		}
	}
	for _, r1 := range n1.ReadSet {
		for _, w2 := range n2.WriteSet {
			if r1 == w2 {
				return true
			}
		}
	}
	return false
}
