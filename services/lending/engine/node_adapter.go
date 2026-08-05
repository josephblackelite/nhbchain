package engine

import (
	"context"
	"errors"
	"math/big"
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

func (a *NodeAdapter) Supply(ctx context.Context, addr, market, amount string) error {
	params := map[string]string{
		"from":   addr,
		"poolId": market,
		"amount": amount,
	}
	return a.invoke(ctx, "lending_supplyNHB", params, nil)
}

func (a *NodeAdapter) Borrow(ctx context.Context, addr, market, amount string) error {
	params := map[string]string{
		"borrower": addr,
		"poolId":   market,
		"amount":   amount,
	}
	return a.invoke(ctx, "lending_borrowNHB", params, nil)
}

func (a *NodeAdapter) Repay(ctx context.Context, addr, market, amount string) error {
	params := map[string]string{
		"from":   addr,
		"poolId": market,
		"amount": amount,
	}
	return a.invoke(ctx, "lending_repayNHB", params, nil)
}

func (a *NodeAdapter) Withdraw(ctx context.Context, addr, market, amount string) error {
	params := map[string]string{
		"from":   addr,
		"poolId": market,
		"amount": amount,
	}
	return a.invoke(ctx, "lending_withdrawNHB", params, nil)
}

func (a *NodeAdapter) Liquidate(ctx context.Context, liquidator, borrower, market, amount string) error {
	params := map[string]string{
		"liquidator": liquidator,
		"borrower":   borrower,
		"poolId":     market,
	}
	if strings.TrimSpace(amount) != "" {
		params["amount"] = amount
	}
	return a.invoke(ctx, "lending_liquidate", params, nil)
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
	var resp lendingPoolsResult
	if err := a.invoke(ctx, "lending_getPools", nil, &resp); err != nil {
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
// (computeHealthFactor in services/lending/server/server.go) already runs
// against GetPosition's response wherever a position is converted to its
// proto form. Derive the same result here from GetPosition + GetMarket,
// both of which call real, working node RPC methods
// (lending_getUserAccount, lending_getMarket), instead of a dead one.
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
		HealthFactor:   computeHealthFactor(position.Account.CollateralZNHB, position.Account.DebtNHB),
	}, nil
}

// computeHealthFactor mirrors services/lending/server/server.go's function
// of the same name exactly (collateral/debt as a decimal ratio, "0" for no
// debt or negative collateral) -- kept in sync deliberately rather than
// exported/shared across packages, since engine is the lower-level package
// server already depends on and importing back the other way would invert
// that dependency.
func computeHealthFactor(collateral, debt string) string {
	collateralValue, ok := new(big.Int).SetString(strings.TrimSpace(collateral), 10)
	if !ok {
		collateralValue = big.NewInt(0)
	}
	debtValue, ok := new(big.Int).SetString(strings.TrimSpace(debt), 10)
	if !ok {
		debtValue = big.NewInt(0)
	}
	if debtValue.Sign() <= 0 {
		return "0"
	}
	if collateralValue.Sign() < 0 {
		return "0"
	}
	rat := new(big.Rat).SetFrac(collateralValue, debtValue)
	decimal := rat.FloatString(18)
	decimal = strings.TrimRight(strings.TrimRight(decimal, "0"), ".")
	if decimal == "" {
		return "0"
	}
	return decimal
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
