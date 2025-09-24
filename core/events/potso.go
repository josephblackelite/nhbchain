package events

import (
	"fmt"
	"math/big"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypePotsoHeartbeat is emitted whenever a heartbeat is accepted by the POTSO module.
	TypePotsoHeartbeat = "potso.heartbeat"
	// TypePotsoStakeLocked is emitted when funds are bonded for POTSO staking.
	TypePotsoStakeLocked = "potso.stake.locked"
	// TypePotsoStakeUnbonded is emitted when bonded funds enter the unbonding queue.
	TypePotsoStakeUnbonded = "potso.stake.unbonded"
	// TypePotsoStakeWithdrawn is emitted when previously bonded funds are withdrawn back to the owner.
	TypePotsoStakeWithdrawn = "potso.stake.withdrawn"
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

func amountString(amount *big.Int) string {
	if amount == nil {
		return "0"
	}
	return amount.String()
}

// PotsoStakeLocked captures a new bonded stake lock.
type PotsoStakeLocked struct {
	Owner  [20]byte
	Amount *big.Int
}

// Event converts the stake lock notification into a generic event.
func (e PotsoStakeLocked) Event() *types.Event {
	addr := crypto.NewAddress(crypto.NHBPrefix, e.Owner[:])
	return &types.Event{
		Type: TypePotsoStakeLocked,
		Attributes: map[string]string{
			"owner":  addr.String(),
			"amount": amountString(e.Amount),
		},
	}
}

// PotsoStakeUnbonded captures an initiated unbonding operation.
type PotsoStakeUnbonded struct {
	Owner      [20]byte
	Amount     *big.Int
	WithdrawAt uint64
}

// Event converts the unbond notification into the generic event representation.
func (e PotsoStakeUnbonded) Event() *types.Event {
	addr := crypto.NewAddress(crypto.NHBPrefix, e.Owner[:])
	return &types.Event{
		Type: TypePotsoStakeUnbonded,
		Attributes: map[string]string{
			"owner":      addr.String(),
			"amount":     amountString(e.Amount),
			"withdrawAt": fmt.Sprintf("%d", e.WithdrawAt),
		},
	}
}

// PotsoStakeWithdrawn captures a completed withdrawal from the staking vault.
type PotsoStakeWithdrawn struct {
	Owner  [20]byte
	Amount *big.Int
}

// Event converts the withdrawal notification into the generic event representation.
func (e PotsoStakeWithdrawn) Event() *types.Event {
	addr := crypto.NewAddress(crypto.NHBPrefix, e.Owner[:])
	return &types.Event{
		Type: TypePotsoStakeWithdrawn,
		Attributes: map[string]string{
			"owner":  addr.String(),
			"amount": amountString(e.Amount),
		},
	}
}
