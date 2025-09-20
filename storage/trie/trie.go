package trie

import (
	// "bytes" // Removed
	// "fmt"   // Removed
	"github.com/ethereum/go-ethereum/crypto"
	// "github.com/ethereum/go-ethereum/common" // Removed
	"github.com/ethereum/go-ethereum/rlp"
)

// A placeholder for a simple key-value database interface.
// This allows our Trie to work with any database (in-memory, disk-based, etc.).
type Database interface {
	Put(key []byte, value []byte) error
	Get(key []byte) ([]byte, error)
}

// Trie is the main structure for the Merkle Patricia Trie.
type Trie struct {
	db   Database
	Root []byte // The hash of the root node
}

// NewTrie creates a new Trie with a given database. The root can be nil for an empty trie.
func NewTrie(db Database, root []byte) *Trie {
	return &Trie{
		db:   db,
		Root: root,
	}
}

// Get retrieves a value from the trie for a given key.
func (t *Trie) Get(key []byte) ([]byte, error) {
	// A full implementation would recursively walk the trie from the root node.
	// For our MVB, we will use a simplified direct lookup.
	// The key for the database lookup is the hash of the key we are looking for.
	return t.db.Get(crypto.Keccak256(key))
}

// Put inserts or updates a value in the trie.
func (t *Trie) Put(key []byte, value []byte) error {
	// For our MVB, we'll implement a simplified "two-level" trie.
	// 1. Create a leaf node with the value.
	leafNode := NewNode()
	// To store the value, we use RLP encoding, just like Ethereum.
	encodedValue, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	leafNode.Value = encodedValue
	leafNodeHash := leafNode.Hash()
	t.db.Put(leafNodeHash, encodedValue) // Store the actual node data

	// 2. In our simplified model, we'll pretend the root is a branch node
	// and just store a mapping from the hashed key to the leaf node's hash.
	// This will update the root hash.
	t.Root = crypto.Keccak256(append(t.Root, leafNodeHash...))

	// Store the value directly using its hashed key. This is what Get() will use.
	return t.db.Put(crypto.Keccak256(key), value)
}
