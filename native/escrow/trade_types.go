package escrow

import (
	"fmt"
	"math/big"
)

// TradeStatus represents the lifecycle phases of a trade orchestrating two
// escrows.
type TradeStatus uint8

const (
	TradeInit TradeStatus = iota
	TradePartialFunded
	TradeFunded
	TradeDisputed
	TradeSettled
	TradeCancelled
	TradeExpired
)

// Trade encapsulates the immutable metadata and runtime status for a two-leg
// escrow trade.
type Trade struct {
        ID          [32]byte
        OfferID     string
        Buyer       [20]byte
        Seller      [20]byte
        QuoteToken  string
        QuoteAmount *big.Int
        EscrowQuote [32]byte
        BaseToken   string
        BaseAmount  *big.Int
        EscrowBase  [32]byte
        Deadline    int64
        CreatedAt   int64
        FundedAt    int64
        SlippageBps uint32
        Status      TradeStatus
}

// Clone returns a deep copy of the trade allowing callers to mutate the result
// without affecting the stored instance.
func (t *Trade) Clone() *Trade {
	if t == nil {
		return nil
	}
	clone := *t
	if t.QuoteAmount != nil {
		clone.QuoteAmount = new(big.Int).Set(t.QuoteAmount)
	} else {
		clone.QuoteAmount = big.NewInt(0)
	}
        if t.BaseAmount != nil {
                clone.BaseAmount = new(big.Int).Set(t.BaseAmount)
        } else {
                clone.BaseAmount = big.NewInt(0)
        }
        return &clone
}

// Valid reports whether the trade status value is supported.
func (s TradeStatus) Valid() bool {
	switch s {
	case TradeInit, TradePartialFunded, TradeFunded, TradeDisputed, TradeSettled, TradeCancelled, TradeExpired:
		return true
	default:
		return false
	}
}

// SanitizeTrade validates and normalises the supplied trade definition,
// returning a cloned instance with canonical token casing and non-nil amount
// fields. The function does not mutate the original value.
func SanitizeTrade(t *Trade) (*Trade, error) {
	if t == nil {
		return nil, fmt.Errorf("trade: nil trade")
	}
	clone := t.Clone()
	normalizedQuote, err := NormalizeToken(clone.QuoteToken)
	if err != nil {
		return nil, err
	}
	clone.QuoteToken = normalizedQuote
	normalizedBase, err := NormalizeToken(clone.BaseToken)
	if err != nil {
		return nil, err
	}
	clone.BaseToken = normalizedBase
	if clone.QuoteAmount == nil {
		clone.QuoteAmount = big.NewInt(0)
	}
	if clone.QuoteAmount.Sign() < 0 {
		return nil, fmt.Errorf("trade: quote amount must be non-negative")
	}
	if clone.BaseAmount == nil {
		clone.BaseAmount = big.NewInt(0)
	}
        if clone.BaseAmount.Sign() < 0 {
                return nil, fmt.Errorf("trade: base amount must be non-negative")
        }
        if clone.SlippageBps > 10_000 {
                return nil, fmt.Errorf("trade: slippage bps out of range")
        }
        if !clone.Status.Valid() {
                return nil, fmt.Errorf("trade: invalid status %d", clone.Status)
        }
        return clone, nil
}
