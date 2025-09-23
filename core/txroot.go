package core

import (
	"github.com/ethereum/go-ethereum/core/rawdb"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/rlp"
	gethtrie "github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"

	"nhbchain/core/types"
)

// ComputeTxRoot builds the canonical transaction trie for the provided
// transactions and returns its root hash. The implementation mirrors Ethereum's
// transaction trie semantics by storing RLP-encoded transactions keyed by their
// index in RLP form.
func ComputeTxRoot(txs []*types.Transaction) ([]byte, error) {
	backend := memorydb.New()
	db := rawdb.NewDatabase(backend)
	trieDB := triedb.NewDatabase(db, triedb.HashDefaults)
	trie, err := gethtrie.New(gethtrie.TrieID(gethtypes.EmptyRootHash), trieDB)
	if err != nil {
		return nil, err
	}
	for i, tx := range txs {
		key := rlp.AppendUint64(nil, uint64(i))
		payload, err := rlp.EncodeToBytes(tx)
		if err != nil {
			return nil, err
		}
		if err := trie.Update(key, payload); err != nil {
			return nil, err
		}
	}
	hash := trie.Hash()
	return hash.Bytes(), nil
}
