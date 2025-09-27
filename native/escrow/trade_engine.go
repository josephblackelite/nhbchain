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

const tradeModuleName = "trade"

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
func (e *TradeEngine) CreateTrade(offerID string, buyer, seller [20]byte, quoteToken string, quoteAmount *big.Int, baseToken string, baseAmount *big.Int, deadline int64, nonce [32]byte) (*Trade, error) {
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
	tradeID := ethcrypto.Keccak256Hash([]byte(strings.TrimSpace(offerID)), buyer[:], seller[:], nonce[:])
	if existing, ok := e.state.TradeGet(tradeID); ok {
		sanitized, err := SanitizeTrade(existing)
		if err != nil {
			return nil, err
		}
		if sanitized.OfferID != offerID || sanitized.Buyer != buyer || sanitized.Seller != seller || sanitized.QuoteToken != normalizedQuote || sanitized.BaseToken != normalizedBase || sanitized.QuoteAmount.Cmp(quoteAmount) != 0 || sanitized.BaseAmount.Cmp(baseAmount) != 0 || sanitized.Deadline != deadline {
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
		return nil
	}
	if err := e.state.TradeSetStatus(trade.ID, newStatus); err != nil {
		return err
	}
	trade.Status = newStatus
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
	if err := e.state.TradeSetStatus(trade.ID, TradeDisputed); err != nil {
		return err
	}
	trade.Status = TradeDisputed
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
	if err := e.state.TradeSetStatus(trade.ID, TradeSettled); err != nil {
		return err
	}
	trade.Status = TradeSettled
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
	if err := e.releaseBaseLeg(trade); err != nil {
		return err
	}
	if err := e.releaseQuoteLeg(trade); err != nil {
		return err
	}
	if err := e.state.TradeSetStatus(trade.ID, TradeSettled); err != nil {
		return err
	}
	trade.Status = TradeSettled
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
		if err := e.state.TradeSetStatus(trade.ID, TradeCancelled); err != nil {
			return err
		}
		trade.Status = TradeCancelled
		e.emit(NewTradeExpiredEvent(trade))
		return nil
	}
	if err := e.state.TradeSetStatus(trade.ID, TradeExpired); err != nil {
		return err
	}
	trade.Status = TradeExpired
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
