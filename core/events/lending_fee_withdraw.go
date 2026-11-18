package events

import (
	"math/big"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeLendingProtocolFeesWithdrawn is emitted when a role holder pays part of a
	// lending pool's protocol-fee counter out of the pool.
	TypeLendingProtocolFeesWithdrawn = "lending.protocolfees.withdrawn"
)

// LendingProtocolFeesWithdrawn records a protocol-fee withdrawal: the pool, the
// account that authorised it, the recipient and the amount.
type LendingProtocolFeesWithdrawn struct {
	PoolID    string
	Admin     crypto.Address
	Recipient crypto.Address
	Amount    *big.Int
}

// EventType returns the canonical event identifier.
func (LendingProtocolFeesWithdrawn) EventType() string { return TypeLendingProtocolFeesWithdrawn }

// Event renders the withdrawal event payload.
func (e LendingProtocolFeesWithdrawn) Event() *types.Event {
	amount := big.NewInt(0)
	if e.Amount != nil {
		amount = new(big.Int).Set(e.Amount)
	}
	return &types.Event{Type: TypeLendingProtocolFeesWithdrawn, Attributes: map[string]string{
		"poolId":    e.PoolID,
		"admin":     e.Admin.String(),
		"recipient": e.Recipient.String(),
		"amountWei": amount.String(),
	}}
}
