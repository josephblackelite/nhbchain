package trie

import (
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	gethtrie "github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb"

	"nhbchain/storage"
)

// Trie wraps go-ethereum's trie implementation to expose a simplified API for the
// rest of the codebase while keeping access to the underlying trie database.
//
// The wrapper keeps track of the last committed root and recreates the
// underlying trie after each commit/reset so the instance can be reused across
// blocks.
//
// The keys passed into Get/Update are expected to be fully hashed (keccak256)
// before insertion, matching the historical behaviour of the project.
//
// Trie is not safe for concurrent use.
type Trie struct {
	store  storage.Database
	trieDB *triedb.Database
	trie   *gethtrie.Trie
	root   common.Hash
}

// NewTrie creates a trie backed by the provided storage and optional root. A nil
// or empty root denotes the empty trie.
func NewTrie(store storage.Database, root []byte) (*Trie, error) {
	trieDB := store.TrieDB()
	rootHash := gethtypes.EmptyRootHash
	if len(root) > 0 {
		rootHash = common.BytesToHash(root)
	}
	underlying, err := gethtrie.New(gethtrie.TrieID(rootHash), trieDB)
	if err != nil {
		return nil, err
	}
	return &Trie{
		store:  store,
		trieDB: trieDB,
		trie:   underlying,
		root:   rootHash,
	}, nil
}

// Get retrieves a value from the trie for the provided key.
func (t *Trie) Get(key []byte) ([]byte, error) {
	return t.trie.Get(key)
}

// TryGet retrieves a value from the trie without panicking when the provided
// key is malformed. The wrapper exists for backwards compatibility with legacy
// callers that historically relied on go-ethereum's helper.
func (t *Trie) TryGet(key []byte) ([]byte, error) {
	return t.trie.Get(key)
}

// Update inserts or updates a value in the trie for the provided key.
func (t *Trie) Update(key, value []byte) error {
	return t.trie.Update(key, value)
}

// TryUpdate inserts or updates a value in the trie while tolerating malformed
// keys. It preserves historical behaviour relied upon by legacy tests.
func (t *Trie) TryUpdate(key, value []byte) error {
	return t.trie.Update(key, value)
}

// Hash returns the root hash of the trie reflecting all in-memory mutations.
func (t *Trie) Hash() common.Hash {
	return t.trie.Hash()
}

// Root returns the last committed root hash.
func (t *Trie) Root() common.Hash {
	return t.root
}

// Reset discards any in-memory changes and reloads the trie at the provided
// root. It is primarily used to roll back speculative state transitions.
func (t *Trie) Reset(root common.Hash) error {
	underlying, err := gethtrie.New(gethtrie.TrieID(root), t.trieDB)
	if err != nil {
		return err
	}
	t.trie = underlying
	t.root = root
	return nil
}

// Copy creates a shallow copy of the trie wrapper using go-ethereum's trie
// cloning facilities. The returned trie shares the same underlying database but
// can be mutated independently.
func (t *Trie) Copy() (*Trie, error) {
	copied := t.trie.Copy()
	return &Trie{
		store:  t.store,
		trieDB: t.trieDB,
		trie:   copied,
		root:   t.root,
	}, nil
}

// Commit persists the trie changes to the backing database and returns the new
// root hash. After committing the wrapper recreates the underlying trie so it
// can be reused for subsequent transitions.
func (t *Trie) Commit(parent common.Hash, blockNumber uint64) (common.Hash, error) {
	newRoot, nodes := t.trie.Commit(false)
	if nodes != nil {
		merged := trienode.NewMergedNodeSet()
		if err := merged.Merge(nodes); err != nil {
			return common.Hash{}, err
		}
		if err := t.trieDB.Update(newRoot, parent, blockNumber, merged, nil); err != nil {
			return common.Hash{}, err
		}
		if err := t.trieDB.Commit(newRoot, false); err != nil {
			return common.Hash{}, err
		}
	}
	underlying, err := gethtrie.New(gethtrie.TrieID(newRoot), t.trieDB)
	if err != nil {
		return common.Hash{}, err
	}
	t.trie = underlying
	t.root = newRoot
	return newRoot, nil
}

// Store exposes the backing storage in case callers need to access it directly.
func (t *Trie) Store() storage.Database {
	return t.store
}

// TrieDB exposes the underlying triedb.Database used by the trie. The returned
// handle can be shared with other state management layers (e.g. go-ethereum's
// state package) to ensure all state mutations operate on the same backing
// storage.
func (t *Trie) TrieDB() *triedb.Database {
	return t.trieDB
}
