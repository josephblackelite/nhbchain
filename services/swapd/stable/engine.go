package stable

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Engine provides a high level facade for pricing and reservation flows.
type Engine struct {
	mu      sync.RWMutex
	assets  map[string]Asset
	limits  Limits
	quotes  map[string]Quote
	reserve map[string]Reservation
	clock   func() time.Time
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
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	assetCfg, ok := e.assets[strings.ToUpper(asset)]
	if !ok {
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
	return quote, nil
}

// ReserveQuote reserves an existing quote for execution.
func (e *Engine) ReserveQuote(ctx context.Context, id, account string, amountIn float64) (Reservation, error) {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	quote, ok := e.quotes[id]
	if !ok {
		return Reservation{}, fmt.Errorf("quote not found")
	}
	now := e.clock()
	if !quote.ExpiresAt.IsZero() && now.After(quote.ExpiresAt) {
		delete(e.quotes, id)
		return Reservation{}, fmt.Errorf("quote expired")
	}
	res := Reservation{
		QuoteID:   quote.ID,
		AmountIn:  amountIn,
		AmountOut: amountIn,
		ExpiresAt: quote.ExpiresAt,
		Account:   account,
	}
	e.reserve[quote.ID] = res
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
	_ = ctx
	e.mu.RLock()
	defer e.mu.RUnlock()
	res, ok := e.reserve[reservationID]
	if !ok {
		return CashOutIntent{}, fmt.Errorf("reservation not found")
	}
	return CashOutIntent{
		ReservationID: reservationID,
		Amount:        res.AmountOut,
		CreatedAt:     e.clock(),
	}, nil
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
