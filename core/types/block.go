package types

import (
	"crypto/sha256"
	"encoding/json"
	// "time" // This line is now removed
)

// BlockHeader represents the header of a block in the NHBCoin blockchain.
// It contains metadata about the block and a commitment to the block's content.
type BlockHeader struct {
	Height    uint64 `json:"height"`
	Timestamp int64  `json:"timestamp"`
	PrevHash  []byte `json:"prevHash"`  // Hash of the previous block's header
	StateRoot []byte `json:"stateRoot"` // Merkle root of the global state after transactions are applied
	TxRoot    []byte `json:"txRoot"`    // Merkle root of the transactions in the block
	Validator []byte `json:"validator"` // Address of the validator who proposed the block
}

// Block represents a full block in the NHBCoin blockchain.
type Block struct {
	Header       *BlockHeader
	Transactions []*Transaction
}

// NewBlock creates a new block from a header and a set of transactions.
func NewBlock(header *BlockHeader, txs []*Transaction) *Block {
	return &Block{
		Header:       header,
		Transactions: txs,
	}
}

// Hash calculates and returns the SHA-256 hash of the block header.
// This hash serves as the block's unique identifier.
func (h *BlockHeader) Hash() ([]byte, error) {
	b, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(b)
	return hash[:], nil
}
