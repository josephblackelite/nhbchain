package events

import (
	"math/big"
	"strconv"

	"nhbchain/core/types"
)

const (
	// TypeLoyaltyProgramCreated is emitted when a loyalty program is first
	// registered on-chain.
	TypeLoyaltyProgramCreated = "loyalty.program.created"
	// TypeLoyaltyProgramUpdated is emitted when a loyalty program's mutable
	// configuration is updated.
	TypeLoyaltyProgramUpdated = "loyalty.program.updated"
	// TypeLoyaltyProgramPaused is emitted when a loyalty program is paused.
	TypeLoyaltyProgramPaused = "loyalty.program.paused"
	// TypeLoyaltyProgramResumed is emitted when a loyalty program is
	// resumed.
	TypeLoyaltyProgramResumed = "loyalty.program.resumed"
	// TypeLoyaltyPaymasterRotated is emitted when a business rotates its
	// paymaster configuration.
	TypeLoyaltyPaymasterRotated = "loyalty.paymaster.rotated"
	// TypeLoyaltySmoothingTick is emitted when the dynamic loyalty controller
	// advances the effective basis points towards the target.
	TypeLoyaltySmoothingTick = "loyalty.smoothing.tick"
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
	ID                 [32]byte
	Active             bool
	AccrualBps         uint32
	MinSpendWei        *big.Int
	CapPerTx           *big.Int
	DailyCapUser       *big.Int
	DailyCapProgram    *big.Int
	EpochCapProgram    *big.Int
	EpochLengthSeconds uint64
	IssuanceCapUser    *big.Int
	StartTime          uint64
	EndTime            uint64
	Pool               [20]byte
	TokenSymbol        string
}

// EventType implements the Event interface.
func (LoyaltyProgramUpdated) EventType() string { return TypeLoyaltyProgramUpdated }

// LoyaltyProgramPaused captures the pause operation for a loyalty program.
type LoyaltyProgramPaused struct {
	ID     [32]byte
	Owner  [20]byte
	Caller [20]byte
}

// EventType implements the Event interface.
func (LoyaltyProgramPaused) EventType() string { return TypeLoyaltyProgramPaused }

// LoyaltyProgramResumed captures the resume operation for a loyalty program.
type LoyaltyProgramResumed struct {
	ID     [32]byte
	Owner  [20]byte
	Caller [20]byte
}

// EventType implements the Event interface.
func (LoyaltyProgramResumed) EventType() string { return TypeLoyaltyProgramResumed }

// LoyaltyPaymasterRotated captures the paymaster rotation for a business.
type LoyaltyPaymasterRotated struct {
	BusinessID   [32]byte
	Owner        [20]byte
	Caller       [20]byte
	OldPaymaster [20]byte
	NewPaymaster [20]byte
}

// EventType implements the Event interface.
func (LoyaltyPaymasterRotated) EventType() string { return TypeLoyaltyPaymasterRotated }

// LoyaltySmoothingTick captures the runtime adjustment of the effective basis
// points applied by the loyalty controller.
type LoyaltySmoothingTick struct {
	EffectiveBps uint32
	TargetBps    uint32
}

// EventType implements the Event interface.
func (LoyaltySmoothingTick) EventType() string { return TypeLoyaltySmoothingTick }

// Event converts the smoothing tick to the generic event payload.
func (t LoyaltySmoothingTick) Event() *types.Event {
	return &types.Event{
		Type: TypeLoyaltySmoothingTick,
		Attributes: map[string]string{
			"effective_bps": strconv.FormatUint(uint64(t.EffectiveBps), 10),
			"target_bps":    strconv.FormatUint(uint64(t.TargetBps), 10),
		},
	}
}
