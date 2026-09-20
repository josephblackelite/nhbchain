package core

// TransactionIndexComplete reports whether the transaction-hash index
// (AddBlock, FindTransactionHeight) covers every block in this node's store:
// BackfillTransactionIndex has finished on this node, which is what its done
// marker records, and every block added since then was indexed by AddBlock in
// the same batch that stored it.
//
// When it is true, a hash that FindTransactionHeight does not know is in no
// block of this chain, so a caller does not have to scan blocks to be sure it
// is absent. When it is false (the backfill has not run to completion on this
// store) an unknown hash may still be in a block the index never saw, and a
// caller has to keep scanning. The index lives in the block store, outside the
// state trie, so this has no bearing on consensus.
func (bc *Blockchain) TransactionIndexComplete() bool {
	done, err := bc.db.Get(txIndexBackfillDoneKey)
	return err == nil && len(done) > 0
}
