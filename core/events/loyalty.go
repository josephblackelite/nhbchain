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
	// TypeLoyaltyCapHit is emitted when a yearly emission cap prevents further
	// payouts.
	TypeLoyaltyCapHit = "loyalty.cap.hit"
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

// LoyaltyCapHit captures the attempted emission and the configured annual cap
// when the protocol refuses to mint additional rewards.
type LoyaltyCapHit struct {
	Attempted *big.Int
	Cap       *big.Int
	Emitted   *big.Int
}

// EventType implements the Event interface.
func (LoyaltyCapHit) EventType() string { return TypeLoyaltyCapHit }

// Event converts the cap hit details to the generic event payload.
func (e LoyaltyCapHit) Event() *types.Event {
	attempted := big.NewInt(0)
	if e.Attempted != nil {
		attempted = new(big.Int).Set(e.Attempted)
	}
	cap := big.NewInt(0)
	if e.Cap != nil {
		cap = new(big.Int).Set(e.Cap)
	}
	emitted := big.NewInt(0)
	if e.Emitted != nil {
		emitted = new(big.Int).Set(e.Emitted)
	}
	remaining := big.NewInt(0).Sub(cap, emitted)
	if remaining.Sign() < 0 {
		remaining = big.NewInt(0)
	}
	return &types.Event{
		Type: TypeLoyaltyCapHit,
		Attributes: map[string]string{
			"attempted": attempted.String(),
			"cap":       cap.String(),
			"emitted":   emitted.String(),
			"remaining": remaining.String(),
		},
	}
}
