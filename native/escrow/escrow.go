package escrow

import "math/big"

// Status represents the current state of an escrow.
type Status byte

const (
	StatusOpen     Status = 0x01 // Escrow is funded and awaiting action
	StatusReleased Status = 0x02 // Funds have been released to the seller
	StatusRefunded Status = 0x03 // Funds have been returned to the buyer
	StatusDisputed Status = 0x04 // A dispute has been raised
)

// Escrow holds the state of a single P2P escrow transaction.
// This will be stored in the state trie.
type Escrow struct {
	ID     []byte   `json:"id"` // A unique identifier for the escrow (e.g., hash of the creation tx)
	Buyer  []byte   `json:"buyer"`
	Seller []byte   `json:"seller"`
	Amount *big.Int `json:"amount"`
	Status Status   `json:"status"`
}
