package escrow

import "math/big"

// Status represents the current state of an escrow.
type Status byte

const (
	StatusOpen       Status = 0x01 // Escrow is listed and available
	StatusInProgress Status = 0x02 // NEW: A buyer has committed, locking the escrow
	StatusReleased   Status = 0x03 // Funds released to seller
	StatusRefunded   Status = 0x04 // Funds returned to seller (if listing is cancelled)
	StatusDisputed   Status = 0x05 // NEW: A dispute has been raised by the buyer
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
