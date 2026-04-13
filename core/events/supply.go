package events

import (
	"math/big"
	"strings"

	"nhbchain/core/types"
)

const (
	// TypeTokenSupply is emitted whenever a token supply changes.
	TypeTokenSupply = "token.supply"

	// SupplyReasonMint identifies mint driven supply increases.
	SupplyReasonMint = "mint"
	// SupplyReasonBurn identifies burn driven supply decreases.
	SupplyReasonBurn = "burn"
)

// TokenSupply captures a supply delta for a fungible token.
type TokenSupply struct {
	Token  string
	Total  *big.Int
	Delta  *big.Int
	Reason string
}

func (TokenSupply) EventType() string { return TypeTokenSupply }

// Event renders the structured supply change event for downstream consumers.
func (e TokenSupply) Event() *types.Event {
	attrs := map[string]string{}
	token := strings.ToUpper(strings.TrimSpace(e.Token))
	if token == "" {
		token = "UNKNOWN"
	}
	attrs["token"] = token

	total := big.NewInt(0)
	if e.Total != nil {
		total = new(big.Int).Set(e.Total)
	}
	attrs["total"] = total.String()

	if e.Delta != nil {
		delta := new(big.Int).Set(e.Delta)
		attrs["delta"] = delta.String()
	}

	reason := strings.TrimSpace(e.Reason)
	if reason != "" {
		attrs["reason"] = reason
	}

	return &types.Event{Type: TypeTokenSupply, Attributes: attrs}
}
