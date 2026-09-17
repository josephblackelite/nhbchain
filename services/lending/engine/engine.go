package engine

import "context"

// Engine describes the operations required by the lending gRPC surface.
//
// The mutating methods (Supply/Withdraw/Borrow/Repay/DepositCollateral/
// WithdrawCollateral/Liquidate) take a signedTxJSON parameter: the caller's
// already-signed NHB_TX_V3_MAINNET transaction, JSON-encoded in the exact
// shape the node's nhb_sendTransaction RPC accepts. The engine relays this
// opaquely -- it never derives, holds, or re-signs a private key. addr/
// market/amount are for request validation and structured logging only;
// the transaction's own recovered signer is the actual on-chain identity.
// Each mutating call returns the mempool-accepted transaction hash, not a
// post-mutation position -- submission is asynchronous, so callers poll
// GetPosition once the transaction confirms.
type Engine interface {
	Supply(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error)
	Borrow(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error)
	Repay(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error)
	Withdraw(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error)
	DepositCollateral(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error)
	WithdrawCollateral(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error)
	Liquidate(ctx context.Context, liquidator, borrower, market, signedTxJSON string) (string, error)
	GetMarket(ctx context.Context, market string) (Market, error)
	ListMarkets(ctx context.Context) ([]Market, error)
	GetPosition(ctx context.Context, addr, market string) (Position, error)
	GetHealth(ctx context.Context, addr string) (Health, error)
}

// Market mirrors the payload returned by the node JSON-RPC handlers.
type Market struct {
	Market         *MarketSnapshot `json:"market,omitempty"`
	RiskParameters RiskParameters  `json:"riskParameters"`
}

// MarketSnapshot captures the accounting state for a lending market using
// decimal encoded strings.
type MarketSnapshot struct {
	PoolID            string `json:"poolID"`
	TotalNHBSupplied  string `json:"totalNHBSupplied"`
	TotalSupplyShares string `json:"totalSupplyShares"`
	TotalNHBBorrowed  string `json:"totalNHBBorrowed"`
	SupplyIndex       string `json:"supplyIndex"`
	BorrowIndex       string `json:"borrowIndex"`
	ReserveFactor     uint64 `json:"reserveFactor"`
	LastUpdateBlock   uint64 `json:"lastUpdateBlock"`
}

// RiskParameters exposes the governance controlled safety configuration.
type RiskParameters struct {
	MaxLTV               uint64         `json:"maxLTV"`
	LiquidationThreshold uint64         `json:"liquidationThreshold"`
	LiquidationBonus     uint64         `json:"liquidationBonus"`
	DeveloperFeeCapBps   uint64         `json:"developerFeeCapBps"`
	BorrowCaps           BorrowCaps     `json:"borrowCaps"`
	Oracle               OracleConfig   `json:"oracle"`
	Pauses               ActionPauses   `json:"pauses"`
	CircuitBreakerActive bool           `json:"circuitBreakerActive"`
	CollateralRouting    CollateralFlow `json:"collateralRouting"`
}

// BorrowCaps aggregates per-block and total borrow ceilings.
type BorrowCaps struct {
	PerBlock       string `json:"perBlock"`
	Total          string `json:"total"`
	UtilisationBps uint64 `json:"utilisationBps"`
}

// OracleConfig describes the accepted freshness window for oracle updates.
type OracleConfig struct {
	MaxAgeBlocks    uint64 `json:"maxAgeBlocks"`
	MaxDeviationBps uint64 `json:"maxDeviationBps"`
}

// ActionPauses captures fine grained pause switches for market operations.
type ActionPauses struct {
	Supply    bool `json:"supply"`
	Borrow    bool `json:"borrow"`
	Repay     bool `json:"repay"`
	Liquidate bool `json:"liquidate"`
}

// CollateralFlow reports the liquidation routing configuration when available.
type CollateralFlow struct {
	LiquidatorBps uint64 `json:"liquidatorBps"`
	DeveloperBps  uint64 `json:"developerBps"`
	ProtocolBps   uint64 `json:"protocolBps"`
}

// Position reflects the structure returned by lending_getUserAccount.
type Position struct {
	Account *AccountSnapshot `json:"account,omitempty"`
}

// AccountSnapshot captures on-ledger balances using decimal encoded strings.
type AccountSnapshot struct {
	Address        string `json:"address,omitempty"`
	CollateralZNHB string `json:"collateralZNHB"`
	SupplyShares   string `json:"supplyShares"`
	DebtNHB        string `json:"debtNHB"`
	ScaledDebt     string `json:"scaledDebt,omitempty"`
}

// Health combines the market snapshot and user account used for risk checks.
type Health struct {
	Market         *MarketSnapshot  `json:"market,omitempty"`
	RiskParameters RiskParameters   `json:"riskParameters"`
	Account        *AccountSnapshot `json:"account,omitempty"`
	HealthFactor   string           `json:"healthFactor"`
}
