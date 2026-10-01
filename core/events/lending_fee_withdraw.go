package events

import (
	"math/big"
	"strings"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeLendingProtocolFeesWithdrawn is emitted when
	// TxTypeLendingWithdrawProtocolFees successfully transfers accrued
	// protocol fees to an admin-designated recipient.
	TypeLendingProtocolFeesWithdrawn = "lending.fees.protocol.withdrawn"
	// TypeLendingDeveloperFeesWithdrawn is
	// TypeLendingProtocolFeesWithdrawn's counterpart for
	// TxTypeLendingWithdrawDeveloperFees.
	TypeLendingDeveloperFeesWithdrawn = "lending.fees.developer.withdrawn"
)

// LendingProtocolFeesWithdrawn records a successful
// TxTypeLendingWithdrawProtocolFees withdrawal: how much, from which pool,
// authorized by which RoleLendingProtocolAdmin signer, and paid to which
// recipient.
type LendingProtocolFeesWithdrawn struct {
	PoolID    string
	Admin     [20]byte
	Recipient crypto.Address
	Amount    *big.Int
}

// EventType returns the canonical event identifier.
func (LendingProtocolFeesWithdrawn) EventType() string { return TypeLendingProtocolFeesWithdrawn }

// Event renders the protocol-fee withdrawal event payload.
func (e LendingProtocolFeesWithdrawn) Event() *types.Event {
	poolID := strings.TrimSpace(e.PoolID)
	if poolID == "" {
		return nil
	}
	amount := big.NewInt(0)
	if e.Amount != nil {
		amount = e.Amount
	}
	attrs := map[string]string{
		"poolId":    poolID,
		"admin":     crypto.MustNewAddress(crypto.NHBPrefix, e.Admin[:]).String(),
		"recipient": e.Recipient.String(),
		"amountWei": amount.String(),
	}
	return &types.Event{Type: TypeLendingProtocolFeesWithdrawn, Attributes: attrs}
}

// LendingDeveloperFeesWithdrawn is LendingProtocolFeesWithdrawn's counterpart
// for TxTypeLendingWithdrawDeveloperFees -- Admin here is the pool's own
// Market.DeveloperOwner, not a RoleLendingProtocolAdmin holder.
type LendingDeveloperFeesWithdrawn struct {
	PoolID    string
	Admin     [20]byte
	Recipient crypto.Address
	Amount    *big.Int
}

// EventType returns the canonical event identifier.
func (LendingDeveloperFeesWithdrawn) EventType() string { return TypeLendingDeveloperFeesWithdrawn }

// Event renders the developer-fee withdrawal event payload.
func (e LendingDeveloperFeesWithdrawn) Event() *types.Event {
	poolID := strings.TrimSpace(e.PoolID)
	if poolID == "" {
		return nil
	}
	amount := big.NewInt(0)
	if e.Amount != nil {
		amount = e.Amount
	}
	attrs := map[string]string{
		"poolId":    poolID,
		"admin":     crypto.MustNewAddress(crypto.NHBPrefix, e.Admin[:]).String(),
		"recipient": e.Recipient.String(),
		"amountWei": amount.String(),
	}
	return &types.Event{Type: TypeLendingDeveloperFeesWithdrawn, Attributes: attrs}
}
