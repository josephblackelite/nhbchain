package events

import (
	"encoding/hex"
	"fmt"
	"math/big"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	TypePotsoPenaltyApplied = "potso.penalty.applied"
)

type PotsoPenaltyApplied struct {
	Hash       [32]byte
	Type       string
	Offender   [20]byte
	DecayBps   uint64
	SlashAmt   *big.Int
	NewWeight  *big.Int
	Block      uint64
	Idempotent bool
}

func (e PotsoPenaltyApplied) Event() *types.Event {
	attrs := map[string]string{
		"hash":       "0x" + hex.EncodeToString(e.Hash[:]),
		"type":       e.Type,
		"offender":   crypto.MustNewAddress(crypto.NHBPrefix, e.Offender[:]).String(),
		"decayPct":   formatBps(e.DecayBps),
		"slashAmt":   amountString(e.SlashAmt),
		"newWeight":  amountString(e.NewWeight),
		"block":      fmt.Sprintf("%d", e.Block),
		"idempotent": fmt.Sprintf("%t", e.Idempotent),
	}
	return &types.Event{Type: TypePotsoPenaltyApplied, Attributes: attrs}
}

func formatBps(bps uint64) string {
	whole := bps / 100
	frac := bps % 100
	return fmt.Sprintf("%d.%02d", whole, frac)
}
