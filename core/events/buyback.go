package events

import (
	"math/big"
	"strconv"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeBuybackAskSubmitted is emitted when a ZNHB holder submits a market
	// ask into the current epoch's treasury buyback auction.
	TypeBuybackAskSubmitted = "buyback.ask.submitted"
	// TypeBuybackRefPriceRecorded is emitted when a verified M-of-N signed
	// reference price is recorded for an epoch's buyback settlement.
	TypeBuybackRefPriceRecorded = "buyback.refprice.recorded"
	// TypeBuybackEpochSettled is emitted once per epoch after the treasury
	// buyback engine's settlement runs (core/buyback_settlement.go).
	TypeBuybackEpochSettled = "buyback.epoch.settled"
)

// BuybackAskSubmitted records a seller's pending market ask for the epoch it
// was escrowed into.
type BuybackAskSubmitted struct {
	Seller    [20]byte
	Epoch     uint64
	AmountWei *big.Int
}

// EventType returns the canonical event identifier.
func (BuybackAskSubmitted) EventType() string { return TypeBuybackAskSubmitted }

// Event renders the buyback ask submission event payload.
func (b BuybackAskSubmitted) Event() *types.Event {
	amount := big.NewInt(0)
	if b.AmountWei != nil {
		amount = new(big.Int).Set(b.AmountWei)
	}
	attrs := map[string]string{
		"epoch":     strconv.FormatUint(b.Epoch, 10),
		"amountWei": amount.String(),
	}
	if b.Seller != ([20]byte{}) {
		attrs["seller"] = crypto.MustNewAddress(crypto.NHBPrefix, b.Seller[:]).String()
	}
	return &types.Event{Type: TypeBuybackAskSubmitted, Attributes: attrs}
}

// BuybackRefPriceRecorded records a verified M-of-N reference-price
// submission accepted for an epoch's settlement.
type BuybackRefPriceRecorded struct {
	Epoch       uint64
	RateNum     *big.Int
	RateDenom   *big.Int
	SignerCount int
}

// EventType returns the canonical event identifier.
func (BuybackRefPriceRecorded) EventType() string { return TypeBuybackRefPriceRecorded }

// Event renders the reference-price recorded event payload.
func (r BuybackRefPriceRecorded) Event() *types.Event {
	num := big.NewInt(0)
	if r.RateNum != nil {
		num = new(big.Int).Set(r.RateNum)
	}
	den := big.NewInt(1)
	if r.RateDenom != nil {
		den = new(big.Int).Set(r.RateDenom)
	}
	attrs := map[string]string{
		"epoch":       strconv.FormatUint(r.Epoch, 10),
		"rateNum":     num.String(),
		"rateDenom":   den.String(),
		"signerCount": strconv.Itoa(r.SignerCount),
	}
	return &types.Event{Type: TypeBuybackRefPriceRecorded, Attributes: attrs}
}

// BuybackEpochSettled records the outcome of one epoch's treasury buyback
// settlement, whether or not any asks were actually filled.
type BuybackEpochSettled struct {
	Epoch              uint64
	AsksFilled         uint64
	TotalZNHBFilled    *big.Int
	TotalNHBPaid       *big.Int
	ReferencePriceUsed bool
	ClearingPrice      string
}

// EventType returns the canonical event identifier.
func (BuybackEpochSettled) EventType() string { return TypeBuybackEpochSettled }

// Event renders the epoch settlement event payload.
func (e BuybackEpochSettled) Event() *types.Event {
	filled := big.NewInt(0)
	if e.TotalZNHBFilled != nil {
		filled = new(big.Int).Set(e.TotalZNHBFilled)
	}
	paid := big.NewInt(0)
	if e.TotalNHBPaid != nil {
		paid = new(big.Int).Set(e.TotalNHBPaid)
	}
	attrs := map[string]string{
		"epoch":              strconv.FormatUint(e.Epoch, 10),
		"asksFilled":         strconv.FormatUint(e.AsksFilled, 10),
		"totalZNHBFilledWei": filled.String(),
		"totalNHBPaidWei":    paid.String(),
		"referencePriceUsed": strconv.FormatBool(e.ReferencePriceUsed),
	}
	if e.ClearingPrice != "" {
		attrs["clearingPrice"] = e.ClearingPrice
	}
	return &types.Event{Type: TypeBuybackEpochSettled, Attributes: attrs}
}
