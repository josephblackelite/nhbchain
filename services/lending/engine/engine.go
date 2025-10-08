package engine

import (
	"context"

	"nhbchain/native/lending"
)

// Engine describes the operations required by the lending gRPC surface.
type Engine interface {
	Supply(ctx context.Context, addr, market, amount string) error
	Borrow(ctx context.Context, addr, market, amount string) error
	Repay(ctx context.Context, addr, market, amount string) error
	Withdraw(ctx context.Context, addr, market, amount string) error
	Liquidate(ctx context.Context, liquidator, borrower, market, amount string) error
	GetMarket(ctx context.Context, market string) (Market, error)
	ListMarkets(ctx context.Context) ([]Market, error)
	GetPosition(ctx context.Context, addr, market string) (Position, error)
	GetHealth(ctx context.Context, addr string) (Health, error)
}

// Market mirrors the JSON payload returned by the legacy RPC handler.
type Market struct {
	Market         *lending.Market        `json:"market"`
	RiskParameters lending.RiskParameters `json:"riskParameters"`
}

// Position reflects the structure returned by lending_getUserAccount.
type Position struct {
	Account *lending.UserAccount `json:"account"`
}

// Health combines the market snapshot and user account used for risk checks.
type Health struct {
	Market         *lending.Market        `json:"market"`
	RiskParameters lending.RiskParameters `json:"riskParameters"`
	Account        *lending.UserAccount   `json:"account"`
}
