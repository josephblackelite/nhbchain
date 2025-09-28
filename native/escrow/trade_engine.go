package escrow

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/events"
	"nhbchain/core/types"
	nativecommon "nhbchain/native/common"
)

var (
	errTradeNilState      = errors.New("trade engine: state not configured")
	errTradeNilEscrow     = errors.New("trade engine: escrow engine not configured")
	errTradeNotFound      = errors.New("trade engine: trade not found")
	errTradeInvalidStatus = errors.New("trade engine: status transition not allowed")
)

const (
        tradeModuleName        = "trade"
        autoRefundSecs  int64  = 900
        defaultSlippageBps     = 0
)

type tradeEngineState interface {
	engineState
	TradePut(*Trade) error
	TradeGet([32]byte) (*Trade, bool)
	TradeSetStatus([32]byte, TradeStatus) error
	TradeIndexEscrow(escrowID [32]byte, tradeID [32]byte) error
	TradeLookupByEscrow(escrowID [32]byte) ([32]byte, bool, error)
	TradeRemoveByEscrow(escrowID [32]byte) error
}

// TradeEngine coordinates a pair of escrows to deliver an atomic two-leg trade.
type TradeEngine struct {
	state   tradeEngineState
	escrow  *Engine
	emitter events.Emitter
	nowFn   func() int64
	pauses  nativecommon.PauseView
}

// NewTradeEngine constructs a trade engine bound to the supplied escrow engine.
func NewTradeEngine(esc *Engine) *TradeEngine {
	return &TradeEngine{
		escrow:  esc,
		emitter: events.NoopEmitter{},
		nowFn:   func() int64 { return time.Now().Unix() },
	}
}

// SetState configures the state backend.
func (e *TradeEngine) SetState(state tradeEngineState) { e.state = state }

// SetEmitter configures the event emitter used by the engine.
func (e *TradeEngine) SetEmitter(emitter events.Emitter) {
	if emitter == nil {
		e.emitter = events.NoopEmitter{}
		return
	}
	e.emitter = emitter
}

func (e *TradeEngine) SetPauses(p nativecommon.PauseView) {
	if e == nil {
		return
	}
	e.pauses = p
	if e.escrow != nil {
		e.escrow.SetPauses(p)
	}
}

// SetNowFunc overrides the time source, primarily used in tests.
func (e *TradeEngine) SetNowFunc(now func() int64) {
	if now == nil {
		e.nowFn = func() int64 { return time.Now().Unix() }
		return
	}
	e.nowFn = now
}

func (e *TradeEngine) emit(evt *types.Event) {
	if e == nil || e.emitter == nil || evt == nil {
		return
	}
	e.emitter.Emit(escrowEvent{evt: evt})
}

func (e *TradeEngine) now() int64 {
	if e == nil || e.nowFn == nil {
		return time.Now().Unix()
	}
	return e.nowFn()
}

// CreateTrade instantiates a pair of escrows and persists the trade definition.
func (e *TradeEngine) CreateTrade(offerID string, buyer, seller [20]byte, quoteToken string, quoteAmount *big.Int, baseToken string, baseAmount *big.Int, deadline int64, slippageBps uint32, nonce [32]byte) (*Trade, error) {
	if e == nil {
		return nil, errTradeNilEscrow
	}
	if e.state == nil {
		return nil, errTradeNilState
	}
	if e.escrow == nil {
		return nil, errTradeNilEscrow
	}
	if err := nativecommon.Guard(e.pauses, tradeModuleName); err != nil {
		return nil, err
	}
	normalizedQuote, err := NormalizeToken(quoteToken)
	if err != nil {
		return nil, err
	}
	normalizedBase, err := NormalizeToken(baseToken)
	if err != nil {
		return nil, err
	}
	if quoteAmount == nil || quoteAmount.Sign() <= 0 {
		return nil, fmt.Errorf("trade: quote amount must be positive")
	}
	if baseAmount == nil || baseAmount.Sign() <= 0 {
		return nil, fmt.Errorf("trade: base amount must be positive")
	}
	now := e.now()
	if deadline < now {
		return nil, fmt.Errorf("trade: deadline before creation time")
	}
        if slippageBps == 0 {
                slippageBps = defaultSlippageBps
        }
        if slippageBps > 10_000 {
                return nil, fmt.Errorf("trade: slippage bps out of range")
        }
        tradeID := ethcrypto.Keccak256Hash([]byte(strings.TrimSpace(offerID)), buyer[:], seller[:], nonce[:])
        if existing, ok := e.state.TradeGet(tradeID); ok {
                sanitized, err := SanitizeTrade(existing)
                if err != nil {
                        return nil, err
                }
                if sanitized.OfferID != offerID || sanitized.Buyer != buyer || sanitized.Seller != seller || sanitized.QuoteToken != normalizedQuote || sanitized.BaseToken != normalizedBase || sanitized.QuoteAmount.Cmp(quoteAmount) != 0 || sanitized.BaseAmount.Cmp(baseAmount) != 0 || sanitized.Deadline != deadline || sanitized.SlippageBps != slippageBps {
                        return nil, fmt.Errorf("trade: identifier already exists with different definition")
                }
                return sanitized.Clone(), nil
        }
	// Create both escrows.
	metaQuote := ethcrypto.Keccak256Hash(tradeID[:], []byte("quote"))
	escQuote, err := e.escrow.Create(buyer, seller, normalizedQuote, quoteAmount, 0, deadline, nil, metaQuote, "")
	if err != nil {
		return nil, err
	}
	metaBase := ethcrypto.Keccak256Hash(tradeID[:], []byte("base"))
	escBase, err := e.escrow.Create(seller, buyer, normalizedBase, baseAmount, 0, deadline, nil, metaBase, "")
	if err != nil {
		return nil, err
	}
	trade := &Trade{
		ID:          tradeID,
		OfferID:     offerID,
		Buyer:       buyer,
		Seller:      seller,
		QuoteToken:  normalizedQuote,
		QuoteAmount: new(big.Int).Set(quoteAmount),
		EscrowQuote: escQuote.ID,
		BaseToken:   normalizedBase,
                BaseAmount:  new(big.Int).Set(baseAmount),
                EscrowBase:  escBase.ID,
                Deadline:    deadline,
                CreatedAt:   now,
                SlippageBps: slippageBps,
                Status:      TradeInit,
        }
	if err := e.state.TradePut(trade); err != nil {
		return nil, err
	}
	if err := e.state.TradeIndexEscrow(escBase.ID, trade.ID); err != nil {
		return nil, err
	}
	if err := e.state.TradeIndexEscrow(escQuote.ID, trade.ID); err != nil {
		return nil, err
	}
	e.emit(NewTradeCreatedEvent(trade))
	return trade.Clone(), nil
}

// HandleEscrowFunded updates trade status for the trade associated with the
// provided escrow identifier.
func (e *TradeEngine) HandleEscrowFunded(escrowID [32]byte) error {
	if e == nil || e.state == nil {
		return errTradeNilState
	}
	if err := nativecommon.Guard(e.pauses, tradeModuleName); err != nil {
		return err
	}
	tradeID, ok, err := e.state.TradeLookupByEscrow(escrowID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return e.OnFundingProgress(tradeID)
}

// OnFundingProgress inspects the legs of the trade and adjusts the trade status
// to reflect the observed funding state.
func (e *TradeEngine) OnFundingProgress(tradeID [32]byte) error {
	trade, err := e.loadTrade(tradeID)
	if err != nil {
		return err
	}
	if err := nativecommon.Guard(e.pauses, tradeModuleName); err != nil {
		return err
	}
	if trade.Status == TradeSettled || trade.Status == TradeExpired || trade.Status == TradeCancelled {
		return nil
	}
	baseEscrow, err := e.loadEscrow(trade.EscrowBase)
	if err != nil {
		return err
	}
	quoteEscrow, err := e.loadEscrow(trade.EscrowQuote)
	if err != nil {
		return err
	}
	baseFunded := baseEscrow.Status == EscrowFunded
	quoteFunded := quoteEscrow.Status == EscrowFunded
	newStatus := trade.Status
	switch {
	case baseFunded && quoteFunded:
		newStatus = TradeFunded
	case baseFunded || quoteFunded:
		newStatus = TradePartialFunded
	default:
		newStatus = TradeInit
	}
        if newStatus == trade.Status {
                if newStatus == TradeFunded && trade.FundedAt == 0 {
                        trade.FundedAt = e.now()
                        if err := e.state.TradePut(trade); err != nil {
                                return err
                        }
                        e.emit(NewTradeFundedEvent(trade))
                }
                return nil
        }
        trade.Status = newStatus
        if newStatus == TradeFunded {
                trade.FundedAt = e.now()
        } else {
                trade.FundedAt = 0
        }
        if err := e.state.TradePut(trade); err != nil {
                return err
        }
        switch newStatus {
        case TradePartialFunded:
                e.emit(NewTradePartialFundedEvent(trade))
        case TradeFunded:
                e.emit(NewTradeFundedEvent(trade))
	}
	return nil
}

// TradeDispute marks the trade as disputed and freezes funded escrows.
func (e *TradeEngine) TradeDispute(tradeID [32]byte, caller [20]byte) error {
	trade, err := e.loadTrade(tradeID)
	if err != nil {
		return err
	}
	if err := nativecommon.Guard(e.pauses, tradeModuleName); err != nil {
		return err
	}
	if caller != trade.Buyer && caller != trade.Seller {
		return fmt.Errorf("trade: unauthorized dispute caller")
	}
	if trade.Status == TradeDisputed {
		return nil
	}
	if trade.Status != TradeFunded && trade.Status != TradePartialFunded {
		return errTradeInvalidStatus
	}
        trade.Status = TradeDisputed
        trade.FundedAt = 0
        if err := e.state.TradePut(trade); err != nil {
                return err
        }
        e.emit(NewTradeDisputedEvent(trade))
        return nil
}

// TradeResolve settles a disputed trade according to the arbitrator outcome.
func (e *TradeEngine) TradeResolve(tradeID [32]byte, outcome string) error {
	trade, err := e.loadTrade(tradeID)
	if err != nil {
		return err
	}
	if err := nativecommon.Guard(e.pauses, tradeModuleName); err != nil {
		return err
	}
	if trade.Status == TradeSettled {
		return nil
	}
	if trade.Status != TradeDisputed {
		return errTradeInvalidStatus
	}
	normalized := strings.ToLower(strings.TrimSpace(outcome))
	switch normalized {
	case "release_both":
		if err := e.releaseBaseLeg(trade); err != nil {
			return err
		}
		if err := e.releaseQuoteLeg(trade); err != nil {
			return err
		}
	case "refund_both":
		if err := e.refundBaseLeg(trade); err != nil {
			return err
		}
		if err := e.refundQuoteLeg(trade); err != nil {
			return err
		}
	case "release_base_refund_quote":
		if err := e.releaseBaseLeg(trade); err != nil {
			return err
		}
		if err := e.refundQuoteLeg(trade); err != nil {
			return err
		}
	case "release_quote_refund_base":
		if err := e.releaseQuoteLeg(trade); err != nil {
			return err
		}
		if err := e.refundBaseLeg(trade); err != nil {
			return err
		}
	default:
		return fmt.Errorf("trade: invalid resolution outcome %s", outcome)
	}
        trade.Status = TradeSettled
        trade.FundedAt = 0
        if err := e.state.TradePut(trade); err != nil {
                return err
        }
        e.emit(NewTradeResolvedEvent(trade, normalized))
        return nil
}

// SettleAtomic releases both legs of the trade atomically once funded.
func (e *TradeEngine) SettleAtomic(tradeID [32]byte) error {
        trade, err := e.loadTrade(tradeID)
        if err != nil {
                return err
        }
	if err := nativecommon.Guard(e.pauses, tradeModuleName); err != nil {
		return err
	}
        if trade.Status == TradeSettled {
                return nil
        }
        if trade.Status == TradeDisputed {
                return fmt.Errorf("trade: disputed trade requires resolution")
        }
        if trade.Deadline > 0 && e.now() > trade.Deadline {
                return fmt.Errorf("trade: settlement deadline elapsed")
        }
        baseEscrow, err := e.loadEscrow(trade.EscrowBase)
        if err != nil {
                return err
        }
        quoteEscrow, err := e.loadEscrow(trade.EscrowQuote)
        if err != nil {
                return err
        }
        if baseEscrow.Status != EscrowFunded || quoteEscrow.Status != EscrowFunded {
                return fmt.Errorf("trade: both escrows must be funded")
        }
        baseBalance, err := e.state.EscrowBalance(trade.EscrowBase, baseEscrow.Token)
        if err != nil {
                return err
        }
        quoteBalance, err := e.state.EscrowBalance(trade.EscrowQuote, quoteEscrow.Token)
        if err != nil {
                return err
        }
        releaseBase, releaseQuote, err := e.computeSettlementAmounts(trade, baseBalance, quoteBalance)
        if err != nil {
                return err
        }
        if err := e.settleLeg(baseEscrow, trade.Buyer, trade.Seller, releaseBase, baseBalance); err != nil {
                return err
        }
        if err := e.settleLeg(quoteEscrow, trade.Seller, trade.Buyer, releaseQuote, quoteBalance); err != nil {
                return err
        }
        trade.Status = TradeSettled
        trade.FundedAt = 0
        if err := e.state.TradePut(trade); err != nil {
                return err
        }
        e.emit(NewTradeSettledEvent(trade))
        return nil
}

// TradeTryExpire refunds any funded leg once the deadline has elapsed.
func (e *TradeEngine) TradeTryExpire(tradeID [32]byte, now int64) error {
        trade, err := e.loadTrade(tradeID)
        if err != nil {
                return err
        }
	if err := nativecommon.Guard(e.pauses, tradeModuleName); err != nil {
		return err
	}
        if trade.Status == TradeSettled || trade.Status == TradeExpired || trade.Status == TradeCancelled {
                return nil
        }
        if trade.Status == TradeFunded && trade.FundedAt > 0 && now >= trade.FundedAt+autoRefundSecs {
                if err := e.refundBaseLeg(trade); err != nil {
                        return err
                }
                if err := e.refundQuoteLeg(trade); err != nil {
                        return err
                }
                trade.Status = TradeExpired
                trade.FundedAt = 0
                if err := e.state.TradePut(trade); err != nil {
                        return err
                }
                e.emit(NewTradeExpiredEvent(trade))
                return nil
        }
        if now < trade.Deadline {
                return nil
        }
        baseEscrow, err := e.loadEscrow(trade.EscrowBase)
        if err != nil {
		return err
	}
	quoteEscrow, err := e.loadEscrow(trade.EscrowQuote)
	if err != nil {
		return err
	}
	baseFunded := baseEscrow.Status == EscrowFunded
	quoteFunded := quoteEscrow.Status == EscrowFunded
	switch {
        case baseFunded && quoteFunded:
                return fmt.Errorf("trade: cannot auto-expire fully funded trade")
        case baseFunded:
                if err := e.refundBaseLeg(trade); err != nil {
                        return err
                }
        case quoteFunded:
                if err := e.refundQuoteLeg(trade); err != nil {
                        return err
                }
        default:
                trade.Status = TradeCancelled
                trade.FundedAt = 0
                if err := e.state.TradePut(trade); err != nil {
                        return err
                }
                e.emit(NewTradeExpiredEvent(trade))
                return nil
        }
        trade.Status = TradeExpired
        trade.FundedAt = 0
        if err := e.state.TradePut(trade); err != nil {
                return err
        }
        e.emit(NewTradeExpiredEvent(trade))
        return nil
}

func (e *TradeEngine) loadTrade(id [32]byte) (*Trade, error) {
	if e == nil || e.state == nil {
		return nil, errTradeNilState
	}
	trade, ok := e.state.TradeGet(id)
	if !ok {
		return nil, errTradeNotFound
	}
	sanitized, err := SanitizeTrade(trade)
	if err != nil {
		return nil, err
	}
	return sanitized, nil
}

func (e *TradeEngine) loadEscrow(id [32]byte) (*Escrow, error) {
	if e == nil || e.state == nil {
		return nil, errTradeNilState
	}
	esc, ok := e.state.EscrowGet(id)
	if !ok {
		return nil, fmt.Errorf("trade: escrow %x not found", id)
	}
	return esc, nil
}

func (e *TradeEngine) releaseBaseLeg(trade *Trade) error {
	baseEscrow, err := e.loadEscrow(trade.EscrowBase)
	if err != nil {
		return err
	}
	if baseEscrow.Status == EscrowReleased {
		return nil
	}
	if baseEscrow.Status != EscrowFunded && baseEscrow.Status != EscrowDisputed {
		return fmt.Errorf("trade: base leg not releasable")
	}
	return e.escrow.Release(trade.EscrowBase, trade.Buyer)
}

func (e *TradeEngine) releaseQuoteLeg(trade *Trade) error {
	quoteEscrow, err := e.loadEscrow(trade.EscrowQuote)
	if err != nil {
		return err
	}
	if quoteEscrow.Status == EscrowReleased {
		return nil
	}
	if quoteEscrow.Status != EscrowFunded && quoteEscrow.Status != EscrowDisputed {
		return fmt.Errorf("trade: quote leg not releasable")
	}
	return e.escrow.Release(trade.EscrowQuote, trade.Seller)
}

func (e *TradeEngine) refundBaseLeg(trade *Trade) error {
	baseEscrow, err := e.loadEscrow(trade.EscrowBase)
	if err != nil {
		return err
	}
	if baseEscrow.Status == EscrowRefunded || baseEscrow.Status == EscrowExpired {
		return nil
	}
	if baseEscrow.Status != EscrowFunded && baseEscrow.Status != EscrowDisputed {
		return fmt.Errorf("trade: base leg not refundable")
	}
	return e.escrow.Refund(trade.EscrowBase, trade.Seller)
}

func (e *TradeEngine) refundQuoteLeg(trade *Trade) error {
        quoteEscrow, err := e.loadEscrow(trade.EscrowQuote)
        if err != nil {
                return err
        }
        if quoteEscrow.Status == EscrowRefunded || quoteEscrow.Status == EscrowExpired {
                return nil
        }
        if quoteEscrow.Status != EscrowFunded && quoteEscrow.Status != EscrowDisputed {
                return fmt.Errorf("trade: quote leg not refundable")
        }
        return e.escrow.Refund(trade.EscrowQuote, trade.Buyer)
}

func (e *TradeEngine) partialRefund(esc *Escrow, recipient [20]byte, amount *big.Int) error {
        if amount == nil || amount.Sign() == 0 {
                return nil
        }
        if amount.Sign() < 0 {
                return fmt.Errorf("trade: negative refund amount")
        }
        vault, err := e.state.EscrowVaultAddress(esc.Token)
        if err != nil {
                return err
        }
        if err := e.escrow.transferToken(vault, recipient, esc.Token, amount); err != nil {
                return err
        }
        return e.state.EscrowDebit(esc.ID, esc.Token, amount)
}

func (e *TradeEngine) settleLeg(esc *Escrow, releaseTo, refundTo [20]byte, releaseAmt, balance *big.Int) error {
        if esc == nil {
                return fmt.Errorf("trade: missing escrow definition")
        }
        if releaseAmt == nil || releaseAmt.Sign() <= 0 {
                return fmt.Errorf("trade: settlement amount must be positive")
        }
        if balance == nil {
                balance = big.NewInt(0)
        }
        if balance.Cmp(releaseAmt) < 0 {
                return fmt.Errorf("trade: insufficient escrow balance for settlement")
        }
        refund := new(big.Int).Sub(balance, releaseAmt)
        if refund.Sign() > 0 {
                if err := e.partialRefund(esc, refundTo, refund); err != nil {
                        return err
                }
        }
        if esc.Amount == nil || esc.Amount.Cmp(releaseAmt) != 0 {
                esc.Amount = new(big.Int).Set(releaseAmt)
                if err := e.escrow.storeEscrow(esc); err != nil {
                        return err
                }
        }
        return e.escrow.Release(esc.ID, releaseTo)
}

func (e *TradeEngine) computeSettlementAmounts(trade *Trade, baseBalance, quoteBalance *big.Int) (*big.Int, *big.Int, error) {
        if trade == nil {
                return nil, nil, fmt.Errorf("trade: nil trade")
        }
        expectedBase := trade.BaseAmount
        expectedQuote := trade.QuoteAmount
        if expectedBase == nil || expectedQuote == nil {
                return nil, nil, fmt.Errorf("trade: missing expected amounts")
        }
        if expectedBase.Sign() <= 0 || expectedQuote.Sign() <= 0 {
                return nil, nil, fmt.Errorf("trade: expected amounts must be positive")
        }
        if baseBalance == nil {
                baseBalance = big.NewInt(0)
        }
        if quoteBalance == nil {
                quoteBalance = big.NewInt(0)
        }
        type candidate struct {
                base  *big.Int
                quote *big.Int
        }
        candidates := make([]candidate, 0, 3)
        if baseBalance.Cmp(expectedBase) >= 0 && quoteBalance.Cmp(expectedQuote) >= 0 {
                candidates = append(candidates, candidate{new(big.Int).Set(expectedBase), new(big.Int).Set(expectedQuote)})
        }
        baseCap := minBigInt(baseBalance, expectedBase)
        if baseCap.Sign() > 0 {
                quoteForBase := new(big.Int).Mul(new(big.Int).Set(baseCap), expectedQuote)
                quoteForBase.Div(quoteForBase, expectedBase)
                if quoteForBase.Sign() > 0 && quoteForBase.Cmp(quoteBalance) <= 0 {
                        candidates = append(candidates, candidate{baseCap, quoteForBase})
                }
        }
        quoteCap := minBigInt(quoteBalance, expectedQuote)
        if quoteCap.Sign() > 0 {
                baseForQuote := new(big.Int).Mul(new(big.Int).Set(quoteCap), expectedBase)
                baseForQuote.Div(baseForQuote, expectedQuote)
                if baseForQuote.Sign() > 0 && baseForQuote.Cmp(baseBalance) <= 0 {
                        candidates = append(candidates, candidate{baseForQuote, quoteCap})
                }
        }
        var best candidate
        for _, cand := range candidates {
                if cand.base == nil || cand.quote == nil {
                        continue
                }
                if cand.base.Sign() == 0 || cand.quote.Sign() == 0 {
                        continue
                }
                if err := ensureSlippage(expectedBase, expectedQuote, cand.base, cand.quote, trade.SlippageBps); err != nil {
                        continue
                }
                if best.base == nil || cand.base.Cmp(best.base) > 0 {
                        best = candidate{new(big.Int).Set(cand.base), new(big.Int).Set(cand.quote)}
                }
        }
        if best.base == nil || best.quote == nil {
                return nil, nil, fmt.Errorf("trade: settlement outside slippage tolerance")
        }
        return best.base, best.quote, nil
}

func minBigInt(a, b *big.Int) *big.Int {
        if a == nil {
                if b == nil {
                        return big.NewInt(0)
                }
                return new(big.Int).Set(b)
        }
        if b == nil {
                return new(big.Int).Set(a)
        }
        if a.Cmp(b) <= 0 {
                return new(big.Int).Set(a)
        }
        return new(big.Int).Set(b)
}

func ensureSlippage(expectedBase, expectedQuote, actualBase, actualQuote *big.Int, slippageBps uint32) error {
        if expectedBase == nil || expectedQuote == nil || actualBase == nil || actualQuote == nil {
                return fmt.Errorf("trade: missing amount for slippage check")
        }
        if expectedBase.Sign() <= 0 || expectedQuote.Sign() <= 0 {
                return fmt.Errorf("trade: expected amounts must be positive")
        }
        if actualBase.Sign() <= 0 || actualQuote.Sign() <= 0 {
                return fmt.Errorf("trade: settlement amounts must be positive")
        }
        lhs := new(big.Int).Mul(actualQuote, expectedBase)
        rhs := new(big.Int).Mul(expectedQuote, actualBase)
        diff := new(big.Int).Sub(lhs, rhs)
        if diff.Sign() < 0 {
                diff.Neg(diff)
        }
        if diff.Sign() == 0 || slippageBps == 0 {
                        if diff.Sign() == 0 {
                                return nil
                        }
                        return fmt.Errorf("trade: slippage exceeds allowance")
        }
        tolerance := new(big.Int).Mul(rhs, big.NewInt(int64(slippageBps)))
        tolerance.Div(tolerance, big.NewInt(10_000))
        if diff.Cmp(tolerance) > 0 {
                return fmt.Errorf("trade: slippage exceeds allowance")
        }
        return nil
}
