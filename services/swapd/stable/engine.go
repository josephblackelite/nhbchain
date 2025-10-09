package stable

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"nhbchain/observability"
)

// Engine provides a high level facade for pricing and reservation flows.
type Engine struct {
	mu        sync.RWMutex
	assets    map[string]Asset
	limits    Limits
	quotes    map[string]*quoteState
	reserve   map[string]*reservationState
	ledger    map[string]*assetLedger
	prices    map[string]pricePoint
	daily     dailyUsage
	clock     func() time.Time
	metrics   *observability.SwapStableMetrics
	tracer    trace.Tracer
	priceAges time.Duration
}

// Asset captures a supported stable asset and its parameters.
type Asset struct {
	Symbol         string
	BasePair       string
	QuotePair      string
	QuoteTTL       time.Duration
	MaxSlippageBps int
	SoftInventory  int64
}

// Limits represent soft throttles for intents.
type Limits struct {
	DailyCap int64
}

// Quote represents a computed exchange quote.
type Quote struct {
	ID        string
	Asset     string
	Price     float64
	ExpiresAt time.Time
}

// Reservation represents a reserved quote.
type Reservation struct {
	QuoteID   string
	AmountIn  float64
	AmountOut float64
	ExpiresAt time.Time
	Account   string
}

// ErrNotSupported is returned when an asset or action is unavailable.
var (
	ErrNotSupported        = errors.New("asset not supported")
	ErrQuoteNotFound       = errors.New("quote not found")
	ErrQuoteExpired        = errors.New("quote expired")
	ErrReservationNotFound = errors.New("reservation not found")
	ErrPriceUnavailable    = errors.New("price unavailable")
	ErrSlippageExceeded    = errors.New("slippage exceeded")
	ErrInsufficientReserve = errors.New("insufficient soft inventory")
	ErrDailyCapExceeded    = errors.New("daily cap exceeded")
	ErrQuoteAmountMismatch = errors.New("quote amount mismatch")
	ErrReservationExpired  = errors.New("reservation expired")
	ErrReservationConsumed = errors.New("reservation already consumed")
)

// NewEngine constructs an Engine from assets and limits.
func NewEngine(assets []Asset, limits Limits) (*Engine, error) {
	if len(assets) == 0 {
		return nil, fmt.Errorf("at least one asset must be configured")
	}
	assetMap := make(map[string]Asset, len(assets))
	for _, asset := range assets {
		if strings.TrimSpace(asset.Symbol) == "" {
			return nil, fmt.Errorf("asset missing symbol")
		}
		assetMap[strings.ToUpper(asset.Symbol)] = asset
	}
	ledger := make(map[string]*assetLedger, len(assetMap))
	for _, cfg := range assetMap {
		ledger[strings.ToUpper(cfg.Symbol)] = &assetLedger{available: float64(cfg.SoftInventory)}
	}
	return &Engine{
		assets:    assetMap,
		limits:    limits,
		quotes:    make(map[string]*quoteState),
		reserve:   make(map[string]*reservationState),
		ledger:    ledger,
		prices:    make(map[string]pricePoint),
		clock:     time.Now,
		metrics:   observability.SwapStable(),
		tracer:    otel.Tracer("swapd/stable"),
		priceAges: 5 * time.Minute,
	}, nil
}

// WithClock overrides the engine clock for deterministic tests.
func (e *Engine) WithClock(clock func() time.Time) {
	if clock == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock = clock
}

// PriceQuote calculates a simple quote placeholder.
func (e *Engine) PriceQuote(ctx context.Context, asset string, amount float64) (Quote, error) {
	start := e.clock()
	ctx, span := e.tracer.Start(ctx, "stable.price_quote",
		trace.WithAttributes(attribute.String("asset", strings.ToUpper(asset))))
	defer span.End()
	e.mu.Lock()
	defer e.mu.Unlock()
	assetCfg, ok := e.assets[strings.ToUpper(asset)]
	if !ok {
		err := ErrNotSupported
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("quote", e.clock().Sub(start), err)
		return Quote{}, ErrNotSupported
	}
	if amount <= 0 {
		err := fmt.Errorf("amount must be positive")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("quote", e.clock().Sub(start), err)
		return Quote{}, err
	}
	price, err := e.lookupPrice(assetCfg)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("quote", e.clock().Sub(start), err)
		return Quote{}, err
	}
	now := e.clock()
	quote := Quote{
		ID:        fmt.Sprintf("q-%d", now.UnixNano()),
		Asset:     assetCfg.Symbol,
		Price:     price.rate,
		ExpiresAt: now.Add(assetCfg.QuoteTTL),
	}
	e.quotes[quote.ID] = &quoteState{
		Quote:  quote,
		Amount: amount,
		Asset:  assetCfg.Symbol,
		Price:  price.rate,
		Issued: now,
	}
	span.SetAttributes(attribute.String("quote.id", quote.ID))
	span.SetStatus(codes.Ok, "quote ready")
	e.metrics.Observe("quote", e.clock().Sub(start), nil)
	return quote, nil
}

// ReserveQuote reserves an existing quote for execution.
func (e *Engine) ReserveQuote(ctx context.Context, id, account string, amountIn float64) (Reservation, error) {
	start := e.clock()
	ctx, span := e.tracer.Start(ctx, "stable.reserve_quote",
		trace.WithAttributes(attribute.String("quote.id", id)))
	defer span.End()
	e.mu.Lock()
	defer e.mu.Unlock()
	quoteState, ok := e.quotes[id]
	if !ok {
		err := ErrQuoteNotFound
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, ErrQuoteNotFound
	}
	now := e.clock()
	quote := quoteState.Quote
	if !quote.ExpiresAt.IsZero() && now.After(quote.ExpiresAt) {
		delete(e.quotes, id)
		err := ErrQuoteExpired
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	if quoteState.Consumed {
		err := ErrQuoteNotFound
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	if amountIn <= 0 {
		err := fmt.Errorf("amount must be positive")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	if !amountMatches(amountIn, quoteState.Amount) {
		err := ErrQuoteAmountMismatch
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	assetCfg := e.assets[quoteState.Asset]
	price, err := e.lookupPrice(assetCfg)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	if exceedsSlippage(quoteState.Price, price.rate, assetCfg.MaxSlippageBps) {
		err := ErrSlippageExceeded
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	amountOut := amountIn * quoteState.Price
	ledger := e.ledger[strings.ToUpper(assetCfg.Symbol)]
	if ledger == nil {
		err := ErrNotSupported
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	if ledger.available < amountOut {
		err := ErrInsufficientReserve
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	if err := e.applyDailyLimit(amountOut, now); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	ledger.available -= amountOut
	ledger.reserved += amountOut
	res := Reservation{
		QuoteID:   quote.ID,
		AmountIn:  amountIn,
		AmountOut: amountOut,
		ExpiresAt: quote.ExpiresAt,
		Account:   account,
	}
	e.reserve[quote.ID] = &reservationState{Reservation: res, Asset: assetCfg.Symbol, Price: quoteState.Price}
	quoteState.Consumed = true
	span.SetAttributes(attribute.String("reservation.id", res.QuoteID))
	span.SetStatus(codes.Ok, "reservation created")
	e.metrics.Observe("reserve", e.clock().Sub(start), nil)
	return res, nil
}

// CashOutIntent is a placeholder for intent creation.
type CashOutIntent struct {
	ID            string
	ReservationID string
	Amount        float64
	CreatedAt     time.Time
}

// CreateCashOutIntent returns a stub intent.
func (e *Engine) CreateCashOutIntent(ctx context.Context, reservationID string) (CashOutIntent, error) {
	start := e.clock()
	ctx, span := e.tracer.Start(ctx, "stable.create_cashout_intent",
		trace.WithAttributes(attribute.String("reservation.id", reservationID)))
	defer span.End()
	e.mu.Lock()
	defer e.mu.Unlock()
	resState, ok := e.reserve[reservationID]
	if !ok {
		err := ErrReservationNotFound
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("cashout_intent", e.clock().Sub(start), err)
		return CashOutIntent{}, err
	}
	res := resState.Reservation
	now := e.clock()
	if !res.ExpiresAt.IsZero() && now.After(res.ExpiresAt) {
		e.releaseReservationLocked(resState, now)
		delete(e.reserve, reservationID)
		err := ErrReservationExpired
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("cashout_intent", e.clock().Sub(start), err)
		return CashOutIntent{}, err
	}
	if resState.IntentCreated {
		err := ErrReservationConsumed
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("cashout_intent", e.clock().Sub(start), err)
		return CashOutIntent{}, err
	}
	ledger := e.ledger[strings.ToUpper(resState.Asset)]
	if ledger == nil {
		err := ErrNotSupported
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("cashout_intent", e.clock().Sub(start), err)
		return CashOutIntent{}, err
	}
	if ledger.reserved < res.AmountOut {
		err := ErrInsufficientReserve
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("cashout_intent", e.clock().Sub(start), err)
		return CashOutIntent{}, err
	}
	ledger.reserved -= res.AmountOut
	ledger.payouts += res.AmountOut
	intent := CashOutIntent{
		ID:            fmt.Sprintf("i-%d", now.UnixNano()),
		ReservationID: reservationID,
		Amount:        res.AmountOut,
		CreatedAt:     now,
	}
	resState.IntentCreated = true
	resState.IntentID = intent.ID
	resState.IntentCreatedAt = now
	span.SetAttributes(
		attribute.Float64("amount", intent.Amount),
		attribute.String("account", res.Account),
	)
	span.SetStatus(codes.Ok, "intent created")
	e.metrics.Observe("cashout_intent", e.clock().Sub(start), nil)
	slog.InfoContext(ctx, "cashout intent created",
		slog.String("reservation_id", reservationID),
		slog.Float64("amount", intent.Amount),
		slog.String("account", res.Account),
	)
	return intent, nil
}

// Status summarises in-memory counters for observability.
type Status struct {
	Quotes       int
	Reservations int
	Assets       int
}

// Status returns a lightweight snapshot of the engine state.
func (e *Engine) Status(ctx context.Context) Status {
	e.mu.RLock()
	defer e.mu.RUnlock()
	quotes := 0
	for _, state := range e.quotes {
		if state == nil {
			continue
		}
		if !state.Consumed {
			quotes++
		}
	}
	reservations := 0
	for _, state := range e.reserve {
		if state == nil {
			continue
		}
		if !state.IntentCreated {
			reservations++
		}
	}
	return Status{Quotes: quotes, Reservations: reservations, Assets: len(e.assets)}
}

type quoteState struct {
	Quote    Quote
	Amount   float64
	Asset    string
	Price    float64
	Issued   time.Time
	Consumed bool
}

type reservationState struct {
	Reservation     Reservation
	Asset           string
	Price           float64
	IntentCreated   bool
	IntentID        string
	IntentCreatedAt time.Time
}

type assetLedger struct {
	available float64
	reserved  float64
	payouts   float64
}

type pricePoint struct {
	rate    float64
	updated time.Time
}

func pairKey(base, quote string) string {
	b := strings.ToUpper(strings.TrimSpace(base))
	q := strings.ToUpper(strings.TrimSpace(quote))
	if b == "" && q == "" {
		return ""
	}
	return b + "/" + q
}

func (e *Engine) lookupPrice(assetCfg Asset) (pricePoint, error) {
	key := pairKey(assetCfg.BasePair, assetCfg.QuotePair)
	price, ok := e.prices[key]
	if !ok || price.rate <= 0 {
		return pricePoint{}, ErrPriceUnavailable
	}
	if e.priceAges > 0 {
		now := e.clock()
		if now.Sub(price.updated) > e.priceAges {
			return pricePoint{}, ErrPriceUnavailable
		}
	}
	return price, nil
}

func (e *Engine) applyDailyLimit(amount float64, now time.Time) error {
	if e.limits.DailyCap <= 0 {
		return nil
	}
	day := now.UTC().Truncate(24 * time.Hour)
	if e.daily.day.IsZero() || !sameDay(day, e.daily.day) {
		e.daily.day = day
		e.daily.amount = 0
	}
	if e.daily.amount+amount > float64(e.limits.DailyCap) {
		return ErrDailyCapExceeded
	}
	e.daily.amount += amount
	return nil
}

func (e *Engine) revertDaily(amount float64, now time.Time) {
	if e.limits.DailyCap <= 0 || amount <= 0 {
		return
	}
	day := now.UTC().Truncate(24 * time.Hour)
	if e.daily.day.IsZero() || !sameDay(day, e.daily.day) {
		return
	}
	e.daily.amount -= amount
	if e.daily.amount < 0 {
		e.daily.amount = 0
	}
}

func (e *Engine) releaseReservationLocked(state *reservationState, now time.Time) {
	if state == nil {
		return
	}
	ledger := e.ledger[strings.ToUpper(state.Asset)]
	if ledger == nil {
		return
	}
	ledger.available += state.Reservation.AmountOut
	if ledger.reserved >= state.Reservation.AmountOut {
		ledger.reserved -= state.Reservation.AmountOut
	}
	e.revertDaily(state.Reservation.AmountOut, now)
}

type dailyUsage struct {
	day    time.Time
	amount float64
}

var epsilon = 1e-6

func amountMatches(a, b float64) bool {
	if b == 0 {
		return a == 0
	}
	diff := math.Abs(a - b)
	return diff <= math.Max(epsilon, math.Abs(b)*epsilon)
}

func exceedsSlippage(reference, observed float64, maxBps int) bool {
	if reference <= 0 || observed <= 0 {
		return true
	}
	if maxBps <= 0 {
		return false
	}
	diff := math.Abs(observed-reference) / reference
	return diff*10000 > float64(maxBps)
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// RecordPrice updates the in-memory price cache used for quoting.
func (e *Engine) RecordPrice(base, quote string, rate float64, updated time.Time) {
	if e == nil {
		return
	}
	if rate <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if updated.IsZero() {
		if e.clock != nil {
			updated = e.clock()
		} else {
			updated = time.Now()
		}
	}
	e.prices[pairKey(base, quote)] = pricePoint{rate: rate, updated: updated}
}

// LedgerBalance returns a snapshot of the treasury ledger for the asset.
func (e *Engine) LedgerBalance(asset string) (float64, float64, float64, bool) {
	if e == nil {
		return 0, 0, 0, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	state, ok := e.ledger[strings.ToUpper(asset)]
	if !ok || state == nil {
		return 0, 0, 0, false
	}
	return state.available, state.reserved, state.payouts, true
}

// SetPriceMaxAge overrides the maximum allowed staleness for oracle prices.
func (e *Engine) SetPriceMaxAge(age time.Duration) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.priceAges = age
}
