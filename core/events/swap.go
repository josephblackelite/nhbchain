package events

import (
	"math/big"
	"strings"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeSwapMinted is emitted whenever a swap voucher mints ZNHB on-chain.
	TypeSwapMinted = "swap.minted"
)

type SwapMinted struct {
	OrderID    string
	Recipient  [20]byte
	Amount     *big.Int
	Fiat       string
	FiatAmount string
	Rate       string
}

func (SwapMinted) EventType() string { return TypeSwapMinted }

func (e SwapMinted) Event() *types.Event {
	amount := big.NewInt(0)
	if e.Amount != nil {
		amount = new(big.Int).Set(e.Amount)
	}
	recipient := ""
	if e.Recipient != ([20]byte{}) {
		recipient = crypto.NewAddress(crypto.NHBPrefix, e.Recipient[:]).String()
	}
	return &types.Event{
		Type: TypeSwapMinted,
		Attributes: map[string]string{
			"orderId":    strings.TrimSpace(e.OrderID),
			"recipient":  recipient,
			"amount":     amount.String(),
			"fiat":       strings.TrimSpace(e.Fiat),
			"fiatAmount": strings.TrimSpace(e.FiatAmount),
			"rate":       strings.TrimSpace(e.Rate),
		},
	}
}
