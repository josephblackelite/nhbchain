package escrow

import "math/big"

// LegacyStatus represents the historical state of an escrow listing as used by
// the initial prototype implementation. The new hardened escrow engine uses the
// EscrowStatus type defined in types.go; the legacy status values remain to keep
// the transitional state processor compiling until that logic is replaced.
type LegacyStatus byte

const (
	LegacyStatusOpen       LegacyStatus = 0x01 // Escrow is listed and available
	LegacyStatusInProgress LegacyStatus = 0x02 // NEW: A buyer has committed, locking the escrow
	LegacyStatusReleased   LegacyStatus = 0x03 // Funds released to seller
	LegacyStatusRefunded   LegacyStatus = 0x04 // Funds returned to seller (if listing is cancelled)
	LegacyStatusDisputed   LegacyStatus = 0x05 // NEW: A dispute has been raised by the buyer
)

// LegacyEscrow holds the state of a single P2P escrow transaction from the
// initial chain prototype. The hardened escrow engine introduced in the
// NHBCHAIN NET-1 milestone uses the Escrow struct defined in types.go. This
// legacy type remains to keep
// the historical state transition logic compiling until that implementation is
// replaced by the new engine.
type LegacyEscrow struct {
	ID     []byte       `json:"id"` // A unique identifier for the escrow (e.g., hash of the creation tx)
	Buyer  []byte       `json:"buyer"`
	Seller []byte       `json:"seller"`
	Amount *big.Int     `json:"amount"`
	Status LegacyStatus `json:"status"`
}
