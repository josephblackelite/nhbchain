package events

import (
	"fmt"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypePotsoHeartbeat is emitted whenever a heartbeat is accepted by the POTSO module.
	TypePotsoHeartbeat = "potso.heartbeat"
)

// PotsoHeartbeat captures a processed heartbeat submission.
type PotsoHeartbeat struct {
	Address     [20]byte
	Timestamp   int64
	BlockHeight uint64
	UptimeDelta uint64
}

// Event converts the heartbeat into the generic event representation.
func (h PotsoHeartbeat) Event() *types.Event {
	addr := crypto.NewAddress(crypto.NHBPrefix, h.Address[:])
	return &types.Event{
		Type: TypePotsoHeartbeat,
		Attributes: map[string]string{
			"address":     addr.String(),
			"timestamp":   fmt.Sprintf("%d", h.Timestamp),
			"blockHeight": fmt.Sprintf("%d", h.BlockHeight),
			"uptimeDelta": fmt.Sprintf("%d", h.UptimeDelta),
		},
	}
}
