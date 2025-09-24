package events

import (
	"encoding/hex"
	"math/big"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	TypeClaimableCreated   = "claimable.created"
	TypeClaimableClaimed   = "claimable.claimed"
	TypeClaimableCancelled = "claimable.cancelled"
	TypeClaimableExpired   = "claimable.expired"
)

type ClaimableCreated struct {
	ID        [32]byte
	Payer     [20]byte
	Token     string
	Amount    *big.Int
	Deadline  int64
	CreatedAt int64
}

func (ClaimableCreated) EventType() string { return TypeClaimableCreated }

func (e ClaimableCreated) Event() *types.Event {
	return &types.Event{
		Type: TypeClaimableCreated,
		Attributes: map[string]string{
			"id":        hex.EncodeToString(e.ID[:]),
			"payer":     crypto.NewAddress(crypto.NHBPrefix, e.Payer[:]).String(),
			"token":     e.Token,
			"amount":    formatAmount(e.Amount),
			"deadline":  intToString(e.Deadline),
			"createdAt": intToString(e.CreatedAt),
		},
	}
}

type ClaimableClaimed struct {
	ID     [32]byte
	Payer  [20]byte
	Payee  [20]byte
	Token  string
	Amount *big.Int
}

func (ClaimableClaimed) EventType() string { return TypeClaimableClaimed }

func (e ClaimableClaimed) Event() *types.Event {
	return &types.Event{
		Type: TypeClaimableClaimed,
		Attributes: map[string]string{
			"id":     hex.EncodeToString(e.ID[:]),
			"payer":  crypto.NewAddress(crypto.NHBPrefix, e.Payer[:]).String(),
			"payee":  crypto.NewAddress(crypto.NHBPrefix, e.Payee[:]).String(),
			"token":  e.Token,
			"amount": formatAmount(e.Amount),
		},
	}
}

type ClaimableCancelled struct {
	ID     [32]byte
	Payer  [20]byte
	Token  string
	Amount *big.Int
}

func (ClaimableCancelled) EventType() string { return TypeClaimableCancelled }

func (e ClaimableCancelled) Event() *types.Event {
	return &types.Event{
		Type: TypeClaimableCancelled,
		Attributes: map[string]string{
			"id":     hex.EncodeToString(e.ID[:]),
			"payer":  crypto.NewAddress(crypto.NHBPrefix, e.Payer[:]).String(),
			"token":  e.Token,
			"amount": formatAmount(e.Amount),
		},
	}
}

type ClaimableExpired struct {
	ID     [32]byte
	Payer  [20]byte
	Token  string
	Amount *big.Int
}

func (ClaimableExpired) EventType() string { return TypeClaimableExpired }

func (e ClaimableExpired) Event() *types.Event {
	return &types.Event{
		Type: TypeClaimableExpired,
		Attributes: map[string]string{
			"id":     hex.EncodeToString(e.ID[:]),
			"payer":  crypto.NewAddress(crypto.NHBPrefix, e.Payer[:]).String(),
			"token":  e.Token,
			"amount": formatAmount(e.Amount),
		},
	}
}

func formatAmount(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}

func intToString(v int64) string {
	return big.NewInt(v).String()
}
