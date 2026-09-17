// Package stablequote defines the wire contract between the validator's RPC
// surface (rpc/swap_stable_handlers.go) and a stable-quote engine that
// implements it -- nothing more. 2026-09-17: split out of
// services/swapd/stable so the public nhbchain repo (this one) can keep
// exposing the *shape* of the stable-quote RPC methods (asset/quote/
// reservation/cash-out request and response fields -- what any RPC client
// already sees on the wire) without also exposing the engine's actual
// reservation/ledger/cash-out IMPLEMENTATION, which now lives in the
// private nhbchain-services repo alongside the rest of the off-chain money
// orchestration. See Engine's doc comment for the exact boundary.
//
// This package contains ONLY data shapes, error sentinels, and the Engine
// interface -- no business logic. Both sides implement/consume the same
// types: the private repo's real stable.Engine (unchanged internal logic,
// just wrapped by a small HTTP server -- see nhbchain-services'
// cmd/stable-quote-service) and this repo's HTTPClient (client.go), which
// cmd/nhb wires in wherever the real engine used to be constructed
// in-process.
package stablequote

import (
	"context"
	"errors"
	"time"
)

// Asset captures a supported stable asset and its parameters -- pure
// configuration, not logic. Mirrors the private engine's own Asset type
// field-for-field; the two must stay in sync by hand (see this package's
// header comment for why there's no shared dependency to enforce it
// automatically).
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

// CashOutIntent records a created stable cash-out intent derived from a
// live reservation awaiting payout.
type CashOutIntent struct {
	ID            string
	ReservationID string
	Amount        float64
	AmountUnits   int64
	Asset         string
	Account       string
	CreatedAt     time.Time
}

// Status is a lightweight snapshot of engine state.
type Status struct {
	Quotes       int
	Reservations int
	Assets       int
}

type QuoteRequest struct {
	Asset  string
	Amount float64
}

type QuoteResponse struct {
	Quote Quote
}

type ReserveRequest struct {
	QuoteID  string
	Account  string
	AmountIn float64
}

type ReserveResponse struct {
	Reservation Reservation
}

type CashOutRequest struct {
	ReservationID string
}

type CashOutResponse struct {
	Intent CashOutIntent
}

// Error sentinels -- same values as the private engine's, so
// errors.Is(err, stablequote.ErrQuoteNotFound) works whether err came back
// wrapped from a local call or unwrapped from an HTTPClient response (see
// client.go's error-code mapping).
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

// Engine is the exact surface rpc/swap_stable_handlers.go calls -- nothing
// more was ever needed from the concrete stable.Engine type it used to
// import directly. Any implementation (the real engine, wrapped locally;
// or HTTPClient, calling out to wherever the real engine actually runs
// now) satisfies this.
type Engine interface {
	Price(ctx context.Context, req QuoteRequest) (QuoteResponse, error)
	Reserve(ctx context.Context, req ReserveRequest) (ReserveResponse, error)
	CashOut(ctx context.Context, req CashOutRequest) (CashOutResponse, error)
	Status(ctx context.Context) Status
	CurrentPrice(base, quote string) (float64, time.Time, bool, bool)
}
