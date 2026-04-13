package events

import (
	"fmt"
	"math/big"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/potso"
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
	// TypePotsoRewardEpoch summarises a processed POTSO reward epoch.
	TypePotsoRewardEpoch = "potso.reward.epoch"
	// TypePotsoRewardReady notifies downstream systems that a claimable reward is prepared for settlement.
	TypePotsoRewardReady = "potso.reward.ready"
	// TypePotsoRewardPaid captures individual reward payouts.
	TypePotsoRewardPaid = "potso.reward.paid"
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
	addr := crypto.MustNewAddress(crypto.NHBPrefix, h.Address[:])
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
	addr := crypto.MustNewAddress(crypto.NHBPrefix, e.Owner[:])
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
	addr := crypto.MustNewAddress(crypto.NHBPrefix, e.Owner[:])
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
	addr := crypto.MustNewAddress(crypto.NHBPrefix, e.Owner[:])
	return &types.Event{
		Type: TypePotsoStakeWithdrawn,
		Attributes: map[string]string{
			"owner":  addr.String(),
			"amount": amountString(e.Amount),
		},
	}
}

// PotsoRewardEpoch captures the aggregated results for a processed reward epoch.
type PotsoRewardEpoch struct {
	Epoch     uint64
	TotalPaid *big.Int
	Winners   uint64
	Emission  *big.Int
	Budget    *big.Int
	Remainder *big.Int
}

// Event converts the reward epoch summary into a generic event representation.
func (e PotsoRewardEpoch) Event() *types.Event {
	attrs := map[string]string{
		"epoch":     fmt.Sprintf("%d", e.Epoch),
		"totalPaid": amountString(e.TotalPaid),
		"winners":   fmt.Sprintf("%d", e.Winners),
	}
	if e.Emission != nil {
		attrs["emission"] = amountString(e.Emission)
	}
	if e.Budget != nil {
		attrs["budget"] = amountString(e.Budget)
	}
	if e.Remainder != nil {
		attrs["remainder"] = amountString(e.Remainder)
	}
	return &types.Event{Type: TypePotsoRewardEpoch, Attributes: attrs}
}

// PotsoRewardReady captures a claimable payout becoming available for settlement.
type PotsoRewardReady struct {
	Epoch   uint64
	Address [20]byte
	Amount  *big.Int
	Mode    potso.RewardPayoutMode
}

// Event converts the ready notification into a generic event representation.
func (e PotsoRewardReady) Event() *types.Event {
	addr := crypto.MustNewAddress(crypto.NHBPrefix, e.Address[:])
	attrs := map[string]string{
		"epoch":   fmt.Sprintf("%d", e.Epoch),
		"address": addr.String(),
		"amount":  amountString(e.Amount),
	}
	if m := e.Mode.Normalise(); m != "" {
		attrs["mode"] = string(m)
	}
	return &types.Event{Type: TypePotsoRewardReady, Attributes: attrs}
}

// PotsoRewardPaid captures an individual reward distribution.
type PotsoRewardPaid struct {
	Epoch   uint64
	Address [20]byte
	Amount  *big.Int
	Mode    potso.RewardPayoutMode
}

// Event converts the payout into a generic event representation.
func (e PotsoRewardPaid) Event() *types.Event {
	addr := crypto.MustNewAddress(crypto.NHBPrefix, e.Address[:])
	attrs := map[string]string{
		"epoch":   fmt.Sprintf("%d", e.Epoch),
		"address": addr.String(),
		"amount":  amountString(e.Amount),
	}
	if m := e.Mode.Normalise(); m != "" {
		attrs["mode"] = string(m)
	}
	return &types.Event{Type: TypePotsoRewardPaid, Attributes: attrs}
}
