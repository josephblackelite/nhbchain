package stable

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	mu      sync.RWMutex
	assets  map[string]Asset
	limits  Limits
	quotes  map[string]Quote
	reserve map[string]Reservation
	clock   func() time.Time
	metrics *observability.SwapStableMetrics
	tracer  trace.Tracer
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
var ErrNotSupported = errors.New("asset not supported")

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
	return &Engine{
		assets:  assetMap,
		limits:  limits,
		quotes:  make(map[string]Quote),
		reserve: make(map[string]Reservation),
		clock:   time.Now,
		metrics: observability.SwapStable(),
		tracer:  otel.Tracer("swapd/stable"),
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
	now := e.clock()
	quote := Quote{
		ID:        fmt.Sprintf("q-%d", now.UnixNano()),
		Asset:     assetCfg.Symbol,
		Price:     amount,
		ExpiresAt: now.Add(assetCfg.QuoteTTL),
	}
	e.quotes[quote.ID] = quote
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
	quote, ok := e.quotes[id]
	if !ok {
		err := fmt.Errorf("quote not found")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, fmt.Errorf("quote not found")
	}
	now := e.clock()
	if !quote.ExpiresAt.IsZero() && now.After(quote.ExpiresAt) {
		delete(e.quotes, id)
		err := fmt.Errorf("quote expired")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("reserve", e.clock().Sub(start), err)
		return Reservation{}, err
	}
	res := Reservation{
		QuoteID:   quote.ID,
		AmountIn:  amountIn,
		AmountOut: amountIn,
		ExpiresAt: quote.ExpiresAt,
		Account:   account,
	}
	e.reserve[quote.ID] = res
	span.SetAttributes(attribute.String("reservation.id", res.QuoteID))
	span.SetStatus(codes.Ok, "reservation created")
	e.metrics.Observe("reserve", e.clock().Sub(start), nil)
	return res, nil
}

// CashOutIntent is a placeholder for intent creation.
type CashOutIntent struct {
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
	e.mu.RLock()
	defer e.mu.RUnlock()
	res, ok := e.reserve[reservationID]
	if !ok {
		err := fmt.Errorf("reservation not found")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("cashout_intent", e.clock().Sub(start), err)
		return CashOutIntent{}, err
	}
	intent := CashOutIntent{
		ReservationID: reservationID,
		Amount:        res.AmountOut,
		CreatedAt:     e.clock(),
	}
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
	return Status{Quotes: len(e.quotes), Reservations: len(e.reserve), Assets: len(e.assets)}
}
