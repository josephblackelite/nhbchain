package server

import (
	"context"

	"nhbchain/services/lending/engine"
)

type fakeEngine struct {
	supplyFn      func(ctx context.Context, addr, market, amount string) error
	borrowFn      func(ctx context.Context, addr, market, amount string) error
	repayFn       func(ctx context.Context, addr, market, amount string) error
	withdrawFn    func(ctx context.Context, addr, market, amount string) error
	liquidateFn   func(ctx context.Context, liquidator, borrower, market, amount string) error
	getMarketFn   func(ctx context.Context, market string) (engine.Market, error)
	listMarketsFn func(ctx context.Context) ([]engine.Market, error)
	getPositionFn func(ctx context.Context, addr, market string) (engine.Position, error)
	getHealthFn   func(ctx context.Context, addr string) (engine.Health, error)
}

func (f *fakeEngine) Supply(ctx context.Context, addr, market, amount string) error {
	if f != nil && f.supplyFn != nil {
		return f.supplyFn(ctx, addr, market, amount)
	}
	return nil
}

func (f *fakeEngine) Borrow(ctx context.Context, addr, market, amount string) error {
	if f != nil && f.borrowFn != nil {
		return f.borrowFn(ctx, addr, market, amount)
	}
	return nil
}

func (f *fakeEngine) Repay(ctx context.Context, addr, market, amount string) error {
	if f != nil && f.repayFn != nil {
		return f.repayFn(ctx, addr, market, amount)
	}
	return nil
}

func (f *fakeEngine) Withdraw(ctx context.Context, addr, market, amount string) error {
	if f != nil && f.withdrawFn != nil {
		return f.withdrawFn(ctx, addr, market, amount)
	}
	return nil
}

func (f *fakeEngine) Liquidate(ctx context.Context, liquidator, borrower, market, amount string) error {
	if f != nil && f.liquidateFn != nil {
		return f.liquidateFn(ctx, liquidator, borrower, market, amount)
	}
	return nil
}

func (f *fakeEngine) GetMarket(ctx context.Context, market string) (engine.Market, error) {
	if f != nil && f.getMarketFn != nil {
		return f.getMarketFn(ctx, market)
	}
	return engine.Market{}, nil
}

func (f *fakeEngine) ListMarkets(ctx context.Context) ([]engine.Market, error) {
	if f != nil && f.listMarketsFn != nil {
		return f.listMarketsFn(ctx)
	}
	return nil, nil
}

func (f *fakeEngine) GetPosition(ctx context.Context, addr, market string) (engine.Position, error) {
	if f != nil && f.getPositionFn != nil {
		return f.getPositionFn(ctx, addr, market)
	}
	return engine.Position{}, nil
}

func (f *fakeEngine) GetHealth(ctx context.Context, addr string) (engine.Health, error) {
	if f != nil && f.getHealthFn != nil {
		return f.getHealthFn(ctx, addr)
	}
	return engine.Health{}, nil
}

type fakeAuthorizer struct {
	called bool
	err    error
}

func (f *fakeAuthorizer) Authorize(ctx context.Context) error {
	f.called = true
	if f.err != nil {
		return f.err
	}
	return nil
}
