package engine

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/rpc/modules"
)

type nodeAdapter struct {
	module *modules.LendingModule
}

// NewNodeAdapter wires a core node into the Engine abstraction expected by the service.
func NewNodeAdapter(node *core.Node) Engine {
	return &nodeAdapter{module: modules.NewLendingModule(node)}
}

func (a *nodeAdapter) Supply(ctx context.Context, addr, market, amount string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parsedAddr, err := parseAddress(addr)
	if err != nil {
		return err
	}
	value, err := parseAmount(amount)
	if err != nil {
		return err
	}
	if _, moduleErr := a.module.SupplyNHB(market, parsedAddr, value); moduleErr != nil {
		return translateModuleError(moduleErr)
	}
	return nil
}

func (a *nodeAdapter) Borrow(ctx context.Context, addr, market, amount string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parsedAddr, err := parseAddress(addr)
	if err != nil {
		return err
	}
	value, err := parseAmount(amount)
	if err != nil {
		return err
	}
	if _, moduleErr := a.module.BorrowNHB(market, parsedAddr, value); moduleErr != nil {
		return translateModuleError(moduleErr)
	}
	return nil
}

func (a *nodeAdapter) Repay(ctx context.Context, addr, market, amount string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parsedAddr, err := parseAddress(addr)
	if err != nil {
		return err
	}
	value, err := parseAmount(amount)
	if err != nil {
		return err
	}
	if _, moduleErr := a.module.RepayNHB(market, parsedAddr, value); moduleErr != nil {
		return translateModuleError(moduleErr)
	}
	return nil
}

func (a *nodeAdapter) Withdraw(ctx context.Context, addr, market, amount string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parsedAddr, err := parseAddress(addr)
	if err != nil {
		return err
	}
	value, err := parseAmount(amount)
	if err != nil {
		return err
	}
	if _, moduleErr := a.module.WithdrawNHB(market, parsedAddr, value); moduleErr != nil {
		return translateModuleError(moduleErr)
	}
	return nil
}

func (a *nodeAdapter) Liquidate(ctx context.Context, liquidator, borrower, market, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	liquidatorAddr, err := parseAddress(liquidator)
	if err != nil {
		return err
	}
	borrowerAddr, err := parseAddress(borrower)
	if err != nil {
		return err
	}
	if _, moduleErr := a.module.Liquidate(market, liquidatorAddr, borrowerAddr); moduleErr != nil {
		return translateModuleError(moduleErr)
	}
	return nil
}

func (a *nodeAdapter) GetMarket(ctx context.Context, market string) (Market, error) {
	if err := ctx.Err(); err != nil {
		return Market{}, err
	}
	snapshot, params, moduleErr := a.module.GetMarket(market)
	if moduleErr != nil {
		return Market{}, translateModuleError(moduleErr)
	}
	return Market{Market: snapshot, RiskParameters: params}, nil
}

func (a *nodeAdapter) ListMarkets(ctx context.Context) ([]Market, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pools, params, moduleErr := a.module.GetPools()
	if moduleErr != nil {
		return nil, translateModuleError(moduleErr)
	}
	results := make([]Market, 0, len(pools))
	for _, pool := range pools {
		results = append(results, Market{Market: pool, RiskParameters: params})
	}
	return results, nil
}

func (a *nodeAdapter) GetPosition(ctx context.Context, addr, market string) (Position, error) {
	if err := ctx.Err(); err != nil {
		return Position{}, err
	}
	parsedAddr, err := parseAddress(addr)
	if err != nil {
		return Position{}, err
	}
	account, moduleErr := a.module.GetUserAccount(market, parsedAddr)
	if moduleErr != nil {
		return Position{}, translateModuleError(moduleErr)
	}
	if account == nil {
		return Position{}, ErrNotFound
	}
	return Position{Account: account}, nil
}

func (a *nodeAdapter) GetHealth(ctx context.Context, addr string) (Health, error) {
	if err := ctx.Err(); err != nil {
		return Health{}, err
	}
	parsedAddr, err := parseAddress(addr)
	if err != nil {
		return Health{}, err
	}
	marketSnapshot, params, moduleErr := a.module.GetMarket("")
	if moduleErr != nil {
		return Health{}, translateModuleError(moduleErr)
	}
	account, moduleErr := a.module.GetUserAccount("", parsedAddr)
	if moduleErr != nil {
		return Health{}, translateModuleError(moduleErr)
	}
	if account == nil {
		return Health{}, ErrNotFound
	}
	return Health{Market: marketSnapshot, RiskParameters: params, Account: account}, nil
}

func parseAddress(addr string) ([20]byte, error) {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return [20]byte{}, fmt.Errorf("address required: %w", ErrInvalidAmount)
	}
	decoded, err := crypto.DecodeAddress(trimmed)
	if err != nil {
		return [20]byte{}, fmt.Errorf("invalid address: %w", ErrInvalidAmount)
	}
	var out [20]byte
	copy(out[:], decoded.Bytes())
	return out, nil
}

func parseAmount(amount string) (*big.Int, error) {
	trimmed := strings.TrimSpace(amount)
	if trimmed == "" {
		return nil, fmt.Errorf("amount required: %w", ErrInvalidAmount)
	}
	value, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount: %w", ErrInvalidAmount)
	}
	if value.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive: %w", ErrInvalidAmount)
	}
	return value, nil
}

func translateModuleError(err *modules.ModuleError) error {
	if err == nil {
		return nil
	}
	switch err.HTTPStatus {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	}
	lower := strings.ToLower(err.Message)
	switch {
	case strings.Contains(lower, "paused"):
		return ErrPaused
	case strings.Contains(lower, "amount") || strings.Contains(lower, "invalid") || strings.Contains(lower, "positive"):
		return ErrInvalidAmount
	case strings.Contains(lower, "health") || strings.Contains(lower, "insufficient") || strings.Contains(lower, "no outstanding debt") || strings.Contains(lower, "not eligible") || strings.Contains(lower, "borrow exceeds"):
		return ErrInsufficientCollateral
	default:
		return ErrInternal
	}
}
