package events

import (
	"encoding/hex"
	"math/big"
	"strconv"
	"strings"

	"nhbchain/core/types"
)

const (
	EventEpochFinalized    = "epoch.finalized"
	EventValidatorsRotated = "validators.rotated"
)

// EpochFinalized signals that an epoch boundary has been reached and the
// composite weights have been recorded.
type EpochFinalized struct {
	Epoch         uint64
	Height        uint64
	FinalizedAt   int64
	TotalWeight   *big.Int
	EligibleCount int
}

// EventType implements the Event interface.
func (EpochFinalized) EventType() string { return EventEpochFinalized }

// Event converts the struct into a types.Event payload.
func (e EpochFinalized) Event() *types.Event {
	total := big.NewInt(0)
	if e.TotalWeight != nil {
		total = new(big.Int).Set(e.TotalWeight)
	}
	attrs := map[string]string{
		"epoch":               strconv.FormatUint(e.Epoch, 10),
		"height":              strconv.FormatUint(e.Height, 10),
		"finalized_at":        strconv.FormatInt(e.FinalizedAt, 10),
		"eligible_validators": strconv.Itoa(e.EligibleCount),
		"total_weight":        total.String(),
	}
	return &types.Event{Type: EventEpochFinalized, Attributes: attrs}
}

// ValidatorsRotated captures a validator set update driven by epoch rotation.
type ValidatorsRotated struct {
	Epoch      uint64
	Validators [][]byte
}

// EventType implements the Event interface.
func (ValidatorsRotated) EventType() string { return EventValidatorsRotated }

// Event converts the rotation into a types.Event payload.
func (e ValidatorsRotated) Event() *types.Event {
	encoded := make([]string, len(e.Validators))
	for i := range e.Validators {
		encoded[i] = "0x" + hex.EncodeToString(e.Validators[i])
	}
	attrs := map[string]string{
		"epoch":      strconv.FormatUint(e.Epoch, 10),
		"validators": strings.Join(encoded, ","),
	}
	return &types.Event{Type: EventValidatorsRotated, Attributes: attrs}
}
