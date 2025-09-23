package events

import "math/big"

const (
	// TypeLoyaltyProgramCreated is emitted when a loyalty program is first
	// registered on-chain.
	TypeLoyaltyProgramCreated = "loyalty.program.created"
	// TypeLoyaltyProgramUpdated is emitted when a loyalty program's mutable
	// configuration is updated.
	TypeLoyaltyProgramUpdated = "loyalty.program.updated"
)

// LoyaltyProgramCreated captures the key metadata of a newly created loyalty
// program.
type LoyaltyProgramCreated struct {
	ID          [32]byte
	Owner       [20]byte
	Pool        [20]byte
	TokenSymbol string
	AccrualBps  uint32
}

// EventType implements the Event interface.
func (LoyaltyProgramCreated) EventType() string { return TypeLoyaltyProgramCreated }

// LoyaltyProgramUpdated captures the mutable configuration of an existing
// loyalty program after an update operation.
type LoyaltyProgramUpdated struct {
	ID           [32]byte
	Active       bool
	AccrualBps   uint32
	MinSpendWei  *big.Int
	CapPerTx     *big.Int
	DailyCapUser *big.Int
	StartTime    uint64
	EndTime      uint64
	Pool         [20]byte
	TokenSymbol  string
}

// EventType implements the Event interface.
func (LoyaltyProgramUpdated) EventType() string { return TypeLoyaltyProgramUpdated }
