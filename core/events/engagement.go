package events

import (
	"fmt"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	TypeEngagementHeartbeat    = "engagement.heartbeat"
	TypeEngagementScoreUpdated = "engagement.score_updated"
)

// EngagementHeartbeat is emitted for each processed heartbeat transaction.
type EngagementHeartbeat struct {
	Address   [20]byte
	DeviceID  string
	Minutes   uint64
	Timestamp int64
}

// EventType implements the Event interface.
func (EngagementHeartbeat) EventType() string { return TypeEngagementHeartbeat }

// Event converts the heartbeat to the generic representation.
func (e EngagementHeartbeat) Event() *types.Event {
	return &types.Event{
		Type: TypeEngagementHeartbeat,
		Attributes: map[string]string{
			"address":   crypto.MustNewAddress(crypto.NHBPrefix, e.Address[:]).String(),
			"device_id": e.DeviceID,
			"minutes":   fmt.Sprintf("%d", e.Minutes),
			"timestamp": fmt.Sprintf("%d", e.Timestamp),
		},
	}
}

// EngagementScoreUpdated is emitted when the EMA score is recomputed for a day.
type EngagementScoreUpdated struct {
	Address  [20]byte
	Day      string
	RawScore uint64
	OldScore uint64
	NewScore uint64
}

// EventType implements the Event interface.
func (EngagementScoreUpdated) EventType() string { return TypeEngagementScoreUpdated }

// Event converts the score update to the generic representation.
func (e EngagementScoreUpdated) Event() *types.Event {
	return &types.Event{
		Type: TypeEngagementScoreUpdated,
		Attributes: map[string]string{
			"address":   crypto.MustNewAddress(crypto.NHBPrefix, e.Address[:]).String(),
			"day":       e.Day,
			"raw":       fmt.Sprintf("%d", e.RawScore),
			"old_score": fmt.Sprintf("%d", e.OldScore),
			"new_score": fmt.Sprintf("%d", e.NewScore),
		},
	}
}
