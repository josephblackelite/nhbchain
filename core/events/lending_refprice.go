package events

import (
	"math/big"
	"strconv"

	"nhbchain/core/types"
)

const (
	// TypeLendingRefPriceRecorded is emitted when a verified M-of-N signed
	// ZNHB/NHB reference price is written into every configured lending
	// market's oracle state.
	TypeLendingRefPriceRecorded = "lending.refprice.recorded"
)

// LendingRefPriceRecorded records a verified M-of-N reference-price
// submission accepted into the lending oracle, and how many markets it was
// applied to.
type LendingRefPriceRecorded struct {
	RateNum     *big.Int
	RateDenom   *big.Int
	Timestamp   uint64
	SignerCount int
	MarketCount int
}

// EventType returns the canonical event identifier.
func (LendingRefPriceRecorded) EventType() string { return TypeLendingRefPriceRecorded }

// Event renders the reference-price recorded event payload.
func (r LendingRefPriceRecorded) Event() *types.Event {
	num := big.NewInt(0)
	if r.RateNum != nil {
		num = new(big.Int).Set(r.RateNum)
	}
	den := big.NewInt(1)
	if r.RateDenom != nil {
		den = new(big.Int).Set(r.RateDenom)
	}
	attrs := map[string]string{
		"rateNum":     num.String(),
		"rateDenom":   den.String(),
		"timestamp":   strconv.FormatUint(r.Timestamp, 10),
		"signerCount": strconv.Itoa(r.SignerCount),
		"marketCount": strconv.Itoa(r.MarketCount),
	}
	return &types.Event{Type: TypeLendingRefPriceRecorded, Attributes: attrs}
}
