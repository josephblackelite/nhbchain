package events

import (
	"math/big"
	"strconv"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeStakeRewardsClaimed is emitted when staking rewards are claimed and minted to an account.
	TypeStakeRewardsClaimed = "stake.rewards_claimed"
)

// StakeRewardsClaimed captures the staking reward payout for an account.
type StakeRewardsClaimed struct {
	Account [20]byte
	Amount  *big.Int
	Periods uint64
	Shares  *big.Int
}

// EventType satisfies the Event interface.
func (StakeRewardsClaimed) EventType() string { return TypeStakeRewardsClaimed }

// Event converts the structured payload into a broadcastable event.
func (e StakeRewardsClaimed) Event() *types.Event {
	attrs := map[string]string{
		"account": crypto.MustNewAddress(crypto.NHBPrefix, e.Account[:]).String(),
		"amount":  formatAmount(e.Amount),
	}
	if e.Periods > 0 {
		attrs["periods"] = strconv.FormatUint(e.Periods, 10)
	}
	if e.Shares != nil && e.Shares.Sign() > 0 {
		attrs["shares"] = e.Shares.String()
	}
	return &types.Event{Type: TypeStakeRewardsClaimed, Attributes: attrs}
}
