package trie

import (
	"github.com/ethereum/go-ethereum/crypto"
)

// Node represents a single node in the Merkle Patricia Trie.
type Node struct {
	// For branch nodes, Children maps the next hex character (0-f) to the hash of the child node.
	Children map[byte][]byte
	// For leaf or extension nodes, Value holds the key's end-part or the actual account data.
	Value []byte
}

// NewNode creates a new, empty trie node.
func NewNode() *Node {
	return &Node{
		Children: make(map[byte][]byte),
	}
}

// Hash calculates the Keccak256 hash of the node, which is used to link nodes together.
func (n *Node) Hash() []byte {
	// A real implementation would RLP-encode the node here before hashing.
	// For our MVB, we'll use a simplified placeholder hash.
	// In a full implementation, this would be:
	// raw, _ := rlp.EncodeToBytes(n)
	// return crypto.Keccak256(raw)

	// Placeholder hashing logic for now:
	if len(n.Children) > 0 {
		// Simplified branch node hash
		var content []byte
		for i := 0; i < 16; i++ {
			if child, ok := n.Children[byte(i)]; ok {
				content = append(content, child...)
			}
		}
		return crypto.Keccak256(content)
	}
	// Leaf/extension node hash
	return crypto.Keccak256(n.Value)
}

// IsLeaf returns true if the node is a leaf node (has a value but no children).
func (n *Node) IsLeaf() bool {
	return len(n.Children) == 0 && n.Value != nil
}
