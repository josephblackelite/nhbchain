package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"nhbchain/services/lending/engine/rpcclient"
)

// NodeAdapter implements Engine by proxying requests to a JSON-RPC endpoint.
//
// It is a thin wrapper around rpcclient.Client that provides error translation
// from the remote node into the sentinel values exposed by this package.
type NodeAdapter struct {
	cli *rpcclient.Client
}

type lendingPoolsResult struct {
	Pools          []*MarketSnapshot `json:"pools"`
	RiskParameters RiskParameters    `json:"riskParameters"`
}

// NewNodeAdapter constructs an Engine backed by the provided JSON-RPC client.
func NewNodeAdapter(cli *rpcclient.Client) *NodeAdapter {
	return &NodeAdapter{cli: cli}
}

// Supply, Withdraw, Borrow, Repay, DepositCollateral, WithdrawCollateral,
// and Liquidate no longer call the disabled lending_* RPC methods (see
// rpc/lending_handlers.go -- those were retired for a shared-JWT signature
// gap: any caller holding the admin JWT could act as any account, since the
// request only carried an "address"/"borrower" string with no signature
// binding it). Every mutation now submits the caller's own pre-signed
// transaction via nhb_sendTransaction instead, so the node -- not
// lendingd -- recovers and enforces the real signer. addr/market/amount
// still get logged/validated by the caller (see server.go) but are never
// sent to the node; only the signed tx is.
func (a *NodeAdapter) Supply(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error) {
	return a.sendSignedTx(ctx, signedTxJSON)
}

func (a *NodeAdapter) Borrow(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error) {
	return a.sendSignedTx(ctx, signedTxJSON)
}

func (a *NodeAdapter) Repay(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error) {
	return a.sendSignedTx(ctx, signedTxJSON)
}

func (a *NodeAdapter) Withdraw(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error) {
	return a.sendSignedTx(ctx, signedTxJSON)
}

func (a *NodeAdapter) DepositCollateral(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error) {
	return a.sendSignedTx(ctx, signedTxJSON)
}

func (a *NodeAdapter) WithdrawCollateral(ctx context.Context, addr, market, amount, signedTxJSON string) (string, error) {
	return a.sendSignedTx(ctx, signedTxJSON)
}

func (a *NodeAdapter) Liquidate(ctx context.Context, liquidator, borrower, market, signedTxJSON string) (string, error) {
	return a.sendSignedTx(ctx, signedTxJSON)
}

// sendSignedTx relays a caller-signed transaction to the node's
// nhb_sendTransaction RPC exactly as received (parsed only enough to
// confirm it is a JSON object, never re-encoded field by field) and returns
// the mempool-accepted transaction hash nhb_sendTransaction responds with.
func (a *NodeAdapter) sendSignedTx(ctx context.Context, signedTxJSON string) (string, error) {
	trimmed := strings.TrimSpace(signedTxJSON)
	if trimmed == "" {
		return "", fmt.Errorf("%w: signed transaction required", ErrInvalidAmount)
	}
	raw := json.RawMessage(trimmed)
	if !json.Valid(raw) {
		return "", fmt.Errorf("%w: signed transaction must be valid JSON", ErrInvalidAmount)
	}
	var hash string
	if err := a.invoke(ctx, "nhb_sendTransaction", raw, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

func (a *NodeAdapter) GetMarket(ctx context.Context, market string) (Market, error) {
	var params any
	if trimmed := strings.TrimSpace(market); trimmed != "" {
		params = map[string]string{"poolId": trimmed}
	}
	var resp Market
	if err := a.invoke(ctx, "lending_getMarket", params, &resp); err != nil {
		return Market{}, err
	}
	return resp, nil
}

func (a *NodeAdapter) ListMarkets(ctx context.Context) ([]Market, error) {
	// NHB-AUDIT-S2: the real, registered node RPC method is "lend_getPools"
	// (see rpc/http.go's method switch) -- "lending_getPools" has never
	// existed, so every call here failed outright.
	var resp lendingPoolsResult
	if err := a.invoke(ctx, "lend_getPools", nil, &resp); err != nil {
		return nil, err
	}
	var markets []Market
	for _, p := range resp.Pools {
		markets = append(markets, Market{
			Market:         p,
			RiskParameters: resp.RiskParameters,
		})
	}
	return markets, nil
}

func (a *NodeAdapter) GetPosition(ctx context.Context, addr, market string) (Position, error) {
	params := map[string]string{
		"address": addr,
		"poolId":  market,
	}
	var resp Position
	if err := a.invoke(ctx, "lending_getUserAccount", params, &resp); err != nil {
		return Position{}, err
	}
	if resp.Account == nil {
		return Position{}, ErrNotFound
	}
	return resp, nil
}

// GetHealth used to call lending_getHealth, a method that has never existed
// on the node's RPC dispatch table (confirmed by grepping rpc/http.go's
// full method switch) -- every call failed. There was never a missing
// computation to add: the exact health-factor math this needs
// (ComputeHealthFactor, this package's health.go) already runs against
// GetPosition's response wherever a position is converted to its proto
// form. Derive the same result here from GetPosition + GetMarket, both of
// which call real, working node RPC methods (lending_getUserAccount,
// lending_getMarket), instead of a dead one.
//
// ComputeHealthFactor is fed position.Account's already oracle-adjusted
// CollateralValueUsd and already fixed-term-loan-inclusive BorrowedValueUsd
// (see that function's doc comment) rather than the raw
// CollateralZNHBWei/Borrowed[] fields a NHB-AUDIT-S2-follow-up audit found
// this call silently reconstructing on its own -- diverging from the
// chain's own positionHealthy/withinMaxLTV math whenever the oracle price
// was not exactly 1:1 or the borrower had an active fixed-term loan.
func (a *NodeAdapter) GetHealth(ctx context.Context, addr string) (Health, error) {
	position, err := a.GetPosition(ctx, addr, "")
	if err != nil {
		return Health{}, err
	}
	market, err := a.GetMarket(ctx, "")
	if err != nil {
		return Health{}, err
	}
	return Health{
		Market:         market.Market,
		RiskParameters: market.RiskParameters,
		Account:        position.Account,
		HealthFactor: ComputeHealthFactor(
			position.Account.CollateralZNHBWei,
			position.Account.CollateralValueUsd,
			position.Account.BorrowedValueUsd,
		),
	}, nil
}

func (a *NodeAdapter) invoke(ctx context.Context, method string, params any, result any) error {
	if a == nil || a.cli == nil {
		return ErrInternal
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var callParams any
	switch p := params.(type) {
	case nil:
		callParams = []any{}
	case []any:
		callParams = p
	default:
		callParams = []any{p}
	}
	if err := a.cli.Call(ctx, method, callParams, result); err != nil {
		return translateError(err)
	}
	return nil
}

func translateError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, "not found"):
		return ErrNotFound
	case strings.Contains(msg, "insufficient") || strings.Contains(msg, "health"):
		return ErrInsufficientCollateral
	case strings.Contains(msg, "paused"):
		return ErrPaused
	case strings.Contains(msg, "amount") || strings.Contains(msg, "invalid") || strings.Contains(msg, "positive"):
		return ErrInvalidAmount
	case strings.Contains(msg, "unauthorized") || strings.Contains(msg, "forbidden"):
		return ErrUnauthorized
	default:
		return ErrInternal
	}
}
