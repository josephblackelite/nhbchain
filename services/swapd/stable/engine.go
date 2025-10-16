package stable

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"nhbchain/observability"
	"nhbchain/services/swapd/storage"
)

// Engine provides a high level facade for pricing and reservation flows.
type Engine struct {
	mu            sync.RWMutex
	assets        map[string]Asset
	limits        Limits
	quotes        map[string]*quoteState
	reserve       map[string]*reservationState
	ledger        map[string]*assetLedger
	prices        map[string]pricePoint
	daily         dailyUsage
	dailyCtx      context.Context
	dailyPersist  DailyUsageStore
	ledgerPersist LedgerReservationStore
	clock         func() time.Time
	metrics       *observability.SwapStableMetrics
	tracer        trace.Tracer
	priceAges     time.Duration
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

// DailyUsageStore persists the cumulative daily usage for minting operations.
type DailyUsageStore interface {
	SaveDailyUsage(ctx context.Context, day time.Time, amount int64) error
	LatestDailyUsage(ctx context.Context) (time.Time, int64, bool, error)
}

// LedgerReservationStore persists ledger balances and outstanding reservations.
type LedgerReservationStore interface {
	SaveLedgerBalance(ctx context.Context, record storage.LedgerBalanceRecord) error
	LoadLedgerBalances(ctx context.Context) ([]storage.LedgerBalanceRecord, error)
	SaveReservation(ctx context.Context, record storage.ReservationRecord) error
	LoadReservations(ctx context.Context) ([]storage.ReservationRecord, error)
	DeleteReservation(ctx context.Context, id string) error
}

// Quote represents a computed exchange quote.
type Quote struct {
	ID        string
	Asset     string
	Price     int64
	ExpiresAt time.Time
}

// Reservation represents a reserved quote.
type Reservation struct {
	QuoteID   string
	AmountIn  int64
	AmountOut int64
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

// NewEngine constructs an Engine from assets and limits, restoring persisted state if available.
func NewEngine(assets []Asset, limits Limits, store LedgerReservationStore) (*Engine, error) {
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
		ledger[strings.ToUpper(cfg.Symbol)] = &assetLedger{available: cfg.SoftInventory * amountScale}
	}
	engine := &Engine{
		assets:    assetMap,
		limits:    limits,
		quotes:    make(map[string]*quoteState),
		reserve:   make(map[string]*reservationState),
		ledger:    ledger,
		prices:    make(map[string]pricePoint),
		dailyCtx:  context.Background(),
		clock:     time.Now,
		metrics:   observability.SwapStable(),
		tracer:    otel.Tracer("swapd/stable"),
		priceAges: 5 * time.Minute,
	}
	if store != nil {
		if err := engine.attachLedgerStore(store); err != nil {
			return nil, err
		}
	}
	return engine, nil
}

func (e *Engine) attachLedgerStore(store LedgerReservationStore) error {
	if store == nil {
		return nil
	}
	ctx := context.Background()
	e.mu.RLock()
	if e.dailyCtx != nil {
		ctx = e.dailyCtx
	}
	e.mu.RUnlock()
	balances, err := store.LoadLedgerBalances(ctx)
	if err != nil {
		return fmt.Errorf("load ledger balances: %w", err)
	}
	reservations, err := store.LoadReservations(ctx)
	if err != nil {
		return fmt.Errorf("load reservations: %w", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.applyLedgerStateLocked(store, balances, reservations)
	return nil
}

// WithDailyUsageStore wires persistence for daily usage accounting.
func (e *Engine) WithDailyUsageStore(store DailyUsageStore) {
	e.mu.Lock()
	e.dailyPersist = store
	ctx := e.dailyCtx
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Unlock()
	if store == nil {
		return
	}
	day, amount, ok, err := store.LatestDailyUsage(ctx)
	if err != nil {
		slog.Error("swapd/stable: load daily usage", "error", err)
	} else if ok {
		e.mu.Lock()
		e.restoreDailyLocked(day, amount)
		e.mu.Unlock()
	}
	if ledgerStore, ok := any(store).(LedgerReservationStore); ok {
		if err := e.attachLedgerStore(ledgerStore); err != nil {
			slog.Error("swapd/stable: load ledger state", "error", err)
		}
	}
}

// RestoreDailyUsage initialises the in-memory counters from persisted state.
func (e *Engine) RestoreDailyUsage(day time.Time, amount int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.restoreDailyLocked(day, amount)
}

func (e *Engine) restoreDailyLocked(day time.Time, amount int64) {
	if amount < 0 {
		amount = 0
	}
	day = day.UTC().Truncate(24 * time.Hour)
	if day.IsZero() {
		e.daily = dailyUsage{}
		return
	}
	e.daily.day = day
	e.daily.amount = amount
}

func (e *Engine) applyLedgerStateLocked(store LedgerReservationStore, balances []storage.LedgerBalanceRecord, reservations []storage.ReservationRecord) {
	if e.ledger == nil {
		e.ledger = make(map[string]*assetLedger)
	}
	e.ledgerPersist = store
	for _, record := range balances {
		asset := strings.ToUpper(strings.TrimSpace(record.Asset))
		if asset == "" {
			continue
		}
		if ledger, ok := e.ledger[asset]; ok && ledger != nil {
			ledger.available = record.Available
			ledger.reserved = record.Reserved
			ledger.payouts = record.Payouts
			continue
		}
		if _, ok := e.assets[asset]; !ok {
			slog.Warn("swapd/stable: skip persisted ledger for unknown asset", "asset", asset)
			continue
		}
		e.ledger[asset] = &assetLedger{available: record.Available, reserved: record.Reserved, payouts: record.Payouts}
	}
	restored := make(map[string]*reservationState, len(reservations))
	for _, record := range reservations {
		asset := strings.ToUpper(strings.TrimSpace(record.Asset))
		if _, ok := e.assets[asset]; !ok {
			slog.Warn("swapd/stable: skip persisted reservation for unknown asset", "asset", asset, "reservation", record.ID)
			continue
		}
		res := Reservation{
			QuoteID:   strings.TrimSpace(record.ID),
			AmountIn:  record.AmountIn,
			AmountOut: record.AmountOut,
			ExpiresAt: record.ExpiresAt,
			Account:   strings.TrimSpace(record.Account),
		}
		if res.QuoteID == "" {
			continue
		}
		restored[res.QuoteID] = &reservationState{
			Reservation:     res,
			Asset:           asset,
			Price:           record.Price,
			IntentCreated:   record.IntentCreated,
			IntentID:        record.IntentID,
			IntentCreatedAt: record.IntentCreatedAt,
		}
	}
	e.reserve = restored
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
	amountUnits, err := toAmountUnits(amount)
	if err != nil {
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
		Amount: amountUnits,
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
	amountUnits, err := toAmountUnits(amountIn)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	if !amountMatches(amountUnits, quoteState.Amount) {
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
	amountOut := mulDivRound(amountUnits, quoteState.Price, priceScale)
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
	prevAvailable := ledger.available
	prevReserved := ledger.reserved
	ledger.available -= amountOut
	ledger.reserved += amountOut
	res := Reservation{
		QuoteID:   quote.ID,
		AmountIn:  amountUnits,
		AmountOut: amountOut,
		ExpiresAt: quote.ExpiresAt,
		Account:   account,
	}
	state := &reservationState{Reservation: res, Asset: assetCfg.Symbol, Price: quoteState.Price}
	e.reserve[quote.ID] = state
	if err := e.persistLedgerLocked(assetCfg.Symbol); err != nil {
		ledger.available = prevAvailable
		ledger.reserved = prevReserved
		delete(e.reserve, quote.ID)
		e.revertDaily(amountOut, now)
		if revertErr := e.persistLedgerLocked(assetCfg.Symbol); revertErr != nil {
			slog.Error("swapd/stable: revert ledger after persistence failure", "error", revertErr, "asset", assetCfg.Symbol)
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	if err := e.persistReservationLocked(state); err != nil {
		delete(e.reserve, quote.ID)
		ledger.available = prevAvailable
		ledger.reserved = prevReserved
		if revertErr := e.persistLedgerLocked(assetCfg.Symbol); revertErr != nil {
			slog.Error("swapd/stable: revert ledger after reservation persistence failure", "error", revertErr, "asset", assetCfg.Symbol)
		}
		if deleteErr := e.deleteReservationLocked(quote.ID); deleteErr != nil {
			slog.Error("swapd/stable: cleanup reservation persistence", "error", deleteErr, "reservation", quote.ID)
		}
		e.revertDaily(amountOut, now)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
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
		if err := e.releaseReservationLocked(resState, now); err != nil {
			slog.Error("swapd/stable: release expired reservation", "error", err, "reservation", reservationID)
		}
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
	prevReserved := ledger.reserved
	prevPayouts := ledger.payouts
	prevIntentCreated := resState.IntentCreated
	prevIntentID := resState.IntentID
	prevIntentAt := resState.IntentCreatedAt
	ledger.reserved -= res.AmountOut
	ledger.payouts += res.AmountOut
	payout := fromAmountUnits(res.AmountOut)
	intent := CashOutIntent{
		ID:            fmt.Sprintf("i-%d", now.UnixNano()),
		ReservationID: reservationID,
		Amount:        payout,
		CreatedAt:     now,
	}
	resState.IntentCreated = true
	resState.IntentID = intent.ID
	resState.IntentCreatedAt = now
	if err := e.persistLedgerLocked(resState.Asset); err != nil {
		ledger.reserved = prevReserved
		ledger.payouts = prevPayouts
		resState.IntentCreated = prevIntentCreated
		resState.IntentID = prevIntentID
		resState.IntentCreatedAt = prevIntentAt
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("cashout_intent", e.clock().Sub(start), err)
		return CashOutIntent{}, err
	}
	if err := e.persistReservationLocked(resState); err != nil {
		ledger.reserved = prevReserved
		ledger.payouts = prevPayouts
		if revertErr := e.persistLedgerLocked(resState.Asset); revertErr != nil {
			slog.Error("swapd/stable: revert ledger after cashout persistence failure", "error", revertErr, "asset", resState.Asset)
		}
		resState.IntentCreated = prevIntentCreated
		resState.IntentID = prevIntentID
		resState.IntentCreatedAt = prevIntentAt
		if revertErr := e.persistReservationLocked(resState); revertErr != nil {
			slog.Error("swapd/stable: revert reservation after persistence failure", "error", revertErr, "reservation", reservationID)
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("cashout_intent", e.clock().Sub(start), err)
		return CashOutIntent{}, err
	}
	span.SetAttributes(
		attribute.Float64("amount", payout),
		attribute.String("account", res.Account),
	)
	span.SetStatus(codes.Ok, "intent created")
	e.metrics.Observe("cashout_intent", e.clock().Sub(start), nil)
	slog.InfoContext(ctx, "cashout intent created",
		slog.String("reservation_id", reservationID),
		slog.Float64("amount", payout),
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
	Amount   int64
	Asset    string
	Price    int64
	Issued   time.Time
	Consumed bool
}

type reservationState struct {
	Reservation     Reservation
	Asset           string
	Price           int64
	IntentCreated   bool
	IntentID        string
	IntentCreatedAt time.Time
}

type assetLedger struct {
	available int64
	reserved  int64
	payouts   int64
}

type pricePoint struct {
	rate    int64
	updated time.Time
}

const (
	amountScale = int64(1_000_000)
	priceScale  = int64(1_000_000_000)
	maxInt64    = int64(^uint64(0) >> 1)
)

func pairKey(base, quote string) string {
	b := strings.ToUpper(strings.TrimSpace(base))
	q := strings.ToUpper(strings.TrimSpace(quote))
	if b == "" && q == "" {
		return ""
	}
	return b + "/" + q
}

func toAmountUnits(amount float64) (int64, error) {
	if amount <= 0 {
		return 0, fmt.Errorf("amount must be positive")
	}
	scaled := math.Round(amount * float64(amountScale))
	units := int64(scaled)
	if units <= 0 {
		return 0, fmt.Errorf("amount must be positive")
	}
	if !withinTolerance(amount, units, amountScale) {
		return 0, fmt.Errorf("amount precision exceeds supported scale")
	}
	return units, nil
}

func fromAmountUnits(units int64) float64 {
	return float64(units) / float64(amountScale)
}

func toRateUnits(rate float64) (int64, error) {
	if rate <= 0 {
		return 0, fmt.Errorf("rate must be positive")
	}
	scaled := math.Round(rate * float64(priceScale))
	units := int64(scaled)
	if units <= 0 {
		return 0, fmt.Errorf("rate must be positive")
	}
	if !withinTolerance(rate, units, priceScale) {
		return 0, fmt.Errorf("rate precision exceeds supported scale")
	}
	return units, nil
}

func fromRateUnits(units int64) float64 {
	return float64(units) / float64(priceScale)
}

// FromAmountUnits converts scaled integer amounts to user friendly values.
func FromAmountUnits(units int64) float64 {
	return fromAmountUnits(units)
}

// FromRateUnits converts scaled integer rates to user friendly values.
func FromRateUnits(units int64) float64 {
	return fromRateUnits(units)
}

func withinTolerance(value float64, units, scale int64) bool {
	recon := float64(units) / float64(scale)
	diff := math.Abs(value - recon)
	tolerance := 1.0 / float64(scale*10)
	return diff <= tolerance
}

func mulDivRound(a, b, denom int64) int64 {
	if denom == 0 {
		return 0
	}
	numerator := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	denomBig := big.NewInt(denom)
	quotient := new(big.Int)
	remainder := new(big.Int)
	quotient.QuoRem(numerator, denomBig, remainder)
	doubled := new(big.Int).Lsh(new(big.Int).Abs(remainder), 1)
	if doubled.Cmp(new(big.Int).Abs(denomBig)) >= 0 {
		if numerator.Sign() >= 0 {
			quotient.Add(quotient, big.NewInt(1))
		} else {
			quotient.Sub(quotient, big.NewInt(1))
		}
	}
	return quotient.Int64()
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
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

func (e *Engine) applyDailyLimit(amount int64, now time.Time) error {
	if e.limits.DailyCap <= 0 {
		return nil
	}
	day := now.UTC().Truncate(24 * time.Hour)
	if e.daily.day.IsZero() || !sameDay(day, e.daily.day) {
		e.daily.day = day
		e.daily.amount = 0
	}
	var capUnits int64
	if e.limits.DailyCap > maxInt64/amountScale {
		capUnits = maxInt64
	} else {
		capUnits = e.limits.DailyCap * amountScale
	}
	if capUnits <= 0 {
		return nil
	}
	if amount > maxInt64-e.daily.amount {
		return ErrDailyCapExceeded
	}
	if e.daily.amount+amount > capUnits {
		return ErrDailyCapExceeded
	}
	e.daily.amount += amount
	if err := e.persistDailyLocked(); err != nil {
		e.daily.amount -= amount
		if e.daily.amount < 0 {
			e.daily.amount = 0
		}
		return err
	}
	return nil
}

func (e *Engine) revertDaily(amount int64, now time.Time) {
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
	if err := e.persistDailyLocked(); err != nil {
		slog.Error("swapd/stable: persist daily usage on revert", "error", err)
	}
}

func (e *Engine) releaseReservationLocked(state *reservationState, now time.Time) error {
	if state == nil {
		return nil
	}
	ledger := e.ledger[strings.ToUpper(state.Asset)]
	if ledger == nil {
		return nil
	}
	prevAvailable := ledger.available
	prevReserved := ledger.reserved
	ledger.available += state.Reservation.AmountOut
	if ledger.reserved >= state.Reservation.AmountOut {
		ledger.reserved -= state.Reservation.AmountOut
	} else {
		ledger.reserved = 0
	}
	if err := e.persistLedgerLocked(state.Asset); err != nil {
		ledger.available = prevAvailable
		ledger.reserved = prevReserved
		if revertErr := e.persistLedgerLocked(state.Asset); revertErr != nil {
			slog.Error("swapd/stable: revert ledger after release failure", "error", revertErr, "asset", state.Asset)
		}
		return err
	}
	e.revertDaily(state.Reservation.AmountOut, now)
	if err := e.deleteReservationLocked(state.Reservation.QuoteID); err != nil {
		slog.Error("swapd/stable: delete reservation on release", "error", err, "reservation", state.Reservation.QuoteID)
	}
	return nil
}

type dailyUsage struct {
	day    time.Time
	amount int64
}

func (e *Engine) persistDailyLocked() error {
	if e.dailyPersist == nil {
		return nil
	}
	if e.daily.day.IsZero() {
		return nil
	}
	ctx := e.dailyCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.dailyPersist.SaveDailyUsage(ctx, e.daily.day, e.daily.amount); err != nil {
		return fmt.Errorf("persist daily usage: %w", err)
	}
	return nil
}

func (e *Engine) persistLedgerLocked(asset string) error {
	if e.ledgerPersist == nil {
		return nil
	}
	assetKey := strings.ToUpper(strings.TrimSpace(asset))
	if assetKey == "" {
		return nil
	}
	state, ok := e.ledger[assetKey]
	if !ok || state == nil {
		return nil
	}
	ctx := e.dailyCtx
	if ctx == nil {
		ctx = context.Background()
	}
	record := storage.LedgerBalanceRecord{
		Asset:     assetKey,
		Available: state.available,
		Reserved:  state.reserved,
		Payouts:   state.payouts,
		UpdatedAt: time.Now().UTC(),
	}
	if err := e.ledgerPersist.SaveLedgerBalance(ctx, record); err != nil {
		return fmt.Errorf("persist ledger: %w", err)
	}
	return nil
}

func (e *Engine) persistReservationLocked(state *reservationState) error {
	if e.ledgerPersist == nil || state == nil {
		return nil
	}
	ctx := e.dailyCtx
	if ctx == nil {
		ctx = context.Background()
	}
	record := storage.ReservationRecord{
		ID:              state.Reservation.QuoteID,
		Asset:           state.Asset,
		AmountIn:        state.Reservation.AmountIn,
		AmountOut:       state.Reservation.AmountOut,
		Price:           state.Price,
		ExpiresAt:       state.Reservation.ExpiresAt,
		Account:         state.Reservation.Account,
		IntentCreated:   state.IntentCreated,
		IntentID:        state.IntentID,
		IntentCreatedAt: state.IntentCreatedAt,
		UpdatedAt:       time.Now().UTC(),
	}
	if err := e.ledgerPersist.SaveReservation(ctx, record); err != nil {
		return fmt.Errorf("persist reservation: %w", err)
	}
	return nil
}

func (e *Engine) deleteReservationLocked(id string) error {
	if e.ledgerPersist == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	ctx := e.dailyCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.ledgerPersist.DeleteReservation(ctx, id); err != nil {
		return fmt.Errorf("delete reservation: %w", err)
	}
	return nil
}

func amountMatches(a, b int64) bool {
	return a == b
}

func exceedsSlippage(reference, observed int64, maxBps int) bool {
	if reference <= 0 || observed <= 0 {
		return true
	}
	if maxBps <= 0 {
		return false
	}
	diff := absInt64(observed - reference)
	return diff*10000 > reference*int64(maxBps)
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
	rateUnits, err := toRateUnits(rate)
	if err != nil {
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
	e.prices[pairKey(base, quote)] = pricePoint{rate: rateUnits, updated: updated}
}

// LedgerBalance returns a snapshot of the treasury ledger for the asset in scaled units.
func (e *Engine) LedgerBalance(asset string) (int64, int64, int64, bool) {
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
