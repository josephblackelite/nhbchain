package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lendingv1 "nhbchain/proto/lending/v1"
	"nhbchain/services/lending/engine"
)

var sentinelErrorCases = []struct {
	name string
	err  error
	code codes.Code
	msg  string
}{
	{name: "not found", err: engine.ErrNotFound, code: codes.NotFound, msg: "resource not found"},
	{name: "paused", err: engine.ErrPaused, code: codes.Unavailable, msg: "operation paused"},
	{name: "unauthorized", err: engine.ErrUnauthorized, code: codes.PermissionDenied, msg: "unauthorized"},
	{name: "invalid amount", err: engine.ErrInvalidAmount, code: codes.InvalidArgument, msg: "invalid amount"},
	{name: "insufficient collateral", err: engine.ErrInsufficientCollateral, code: codes.ResourceExhausted, msg: "insufficient collateral"},
	{name: "internal", err: engine.ErrInternal, code: codes.Internal, msg: "internal error"},
}

func wrapError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("wrap: %w", err)
}

func TestService_SupplyAsset(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	req := &lendingv1.SupplyAssetRequest{
		Account: "  alice  ",
		Market:  &lendingv1.MarketKey{Symbol: "  nhb  "},
		Amount:  "  1000  ",
	}

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		auth := &fakeAuthorizer{}
		eng := &fakeEngine{
			supplyFn: func(_ context.Context, addr, market, amount string) error {
				if addr != "alice" {
					t.Fatalf("unexpected account: %q", addr)
				}
				if market != "nhb" {
					t.Fatalf("unexpected market: %q", market)
				}
				if amount != "1000" {
					t.Fatalf("unexpected amount: %q", amount)
				}
				return nil
			},
		}
		svc := &Service{engine: eng, auth: auth}

		resp, err := svc.SupplyAsset(ctx, req)
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if resp == nil {
			t.Fatalf("expected response")
		}
		if !auth.called {
			t.Fatalf("expected authorizer to be called")
		}
	})

	for _, tc := range sentinelErrorCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			auth := &fakeAuthorizer{}
			eng := &fakeEngine{
				supplyFn: func(context.Context, string, string, string) error {
					return wrapError(tc.err)
				},
			}
			svc := &Service{engine: eng, auth: auth}

			_, err := svc.SupplyAsset(ctx, req)
			if err == nil {
				t.Fatalf("expected error")
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected gRPC status error")
			}
			if st.Code() != tc.code {
				t.Fatalf("expected code %s, got %s", tc.code, st.Code())
			}
			if st.Message() != tc.msg {
				t.Fatalf("expected message %q, got %q", tc.msg, st.Message())
			}
			if !auth.called {
				t.Fatalf("expected authorizer to be called")
			}
		})
	}

	t.Run("authorization error", func(t *testing.T) {
		t.Parallel()

		auth := &fakeAuthorizer{err: status.Error(codes.PermissionDenied, "nope")}
		svc := &Service{engine: &fakeEngine{}, auth: auth}
		_, err := svc.SupplyAsset(ctx, req)
		if !errors.Is(err, auth.err) {
			t.Fatalf("expected authorization error, got %v", err)
		}
	})
}

func TestService_WithdrawAsset(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	req := &lendingv1.WithdrawAssetRequest{
		Account: "  bob  ",
		Market:  &lendingv1.MarketKey{Symbol: "  usdc  "},
		Amount:  "  500  ",
	}

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		auth := &fakeAuthorizer{}
		eng := &fakeEngine{
			withdrawFn: func(_ context.Context, addr, market, amount string) error {
				if addr != "bob" {
					t.Fatalf("unexpected account: %q", addr)
				}
				if market != "usdc" {
					t.Fatalf("unexpected market: %q", market)
				}
				if amount != "500" {
					t.Fatalf("unexpected amount: %q", amount)
				}
				return nil
			},
		}
		svc := &Service{engine: eng, auth: auth}

		resp, err := svc.WithdrawAsset(ctx, req)
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if resp == nil {
			t.Fatalf("expected response")
		}
		if !auth.called {
			t.Fatalf("expected authorizer to be called")
		}
	})

	for _, tc := range sentinelErrorCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			auth := &fakeAuthorizer{}
			eng := &fakeEngine{
				withdrawFn: func(context.Context, string, string, string) error {
					return wrapError(tc.err)
				},
			}
			svc := &Service{engine: eng, auth: auth}

			_, err := svc.WithdrawAsset(ctx, req)
			if err == nil {
				t.Fatalf("expected error")
			}
			st := status.Convert(err)
			if st.Code() != tc.code {
				t.Fatalf("expected code %s, got %s", tc.code, st.Code())
			}
			if st.Message() != tc.msg {
				t.Fatalf("expected message %q, got %q", tc.msg, st.Message())
			}
			if !auth.called {
				t.Fatalf("expected authorizer to be called")
			}
		})
	}
}

func TestService_BorrowAsset(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	req := &lendingv1.BorrowAssetRequest{
		Account: "  carol  ",
		Market:  &lendingv1.MarketKey{Symbol: "  nhb  "},
		Amount:  "  42  ",
	}

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		auth := &fakeAuthorizer{}
		eng := &fakeEngine{
			borrowFn: func(_ context.Context, addr, market, amount string) error {
				if addr != "carol" {
					t.Fatalf("unexpected account: %q", addr)
				}
				if market != "nhb" {
					t.Fatalf("unexpected market: %q", market)
				}
				if amount != "42" {
					t.Fatalf("unexpected amount: %q", amount)
				}
				return nil
			},
		}
		svc := &Service{engine: eng, auth: auth}

		resp, err := svc.BorrowAsset(ctx, req)
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if resp == nil {
			t.Fatalf("expected response")
		}
		if !auth.called {
			t.Fatalf("expected authorizer to be called")
		}
	})

	for _, tc := range sentinelErrorCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			auth := &fakeAuthorizer{}
			eng := &fakeEngine{
				borrowFn: func(context.Context, string, string, string) error {
					return wrapError(tc.err)
				},
			}
			svc := &Service{engine: eng, auth: auth}

			_, err := svc.BorrowAsset(ctx, req)
			if err == nil {
				t.Fatalf("expected error")
			}
			st := status.Convert(err)
			if st.Code() != tc.code {
				t.Fatalf("expected code %s, got %s", tc.code, st.Code())
			}
			if st.Message() != tc.msg {
				t.Fatalf("expected message %q, got %q", tc.msg, st.Message())
			}
			if !auth.called {
				t.Fatalf("expected authorizer to be called")
			}
		})
	}
}

func TestService_RepayAsset(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	req := &lendingv1.RepayAssetRequest{
		Account: "  dave  ",
		Market:  &lendingv1.MarketKey{Symbol: "  nhb  "},
		Amount:  "  7  ",
	}

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		auth := &fakeAuthorizer{}
		eng := &fakeEngine{
			repayFn: func(_ context.Context, addr, market, amount string) error {
				if addr != "dave" {
					t.Fatalf("unexpected account: %q", addr)
				}
				if market != "nhb" {
					t.Fatalf("unexpected market: %q", market)
				}
				if amount != "7" {
					t.Fatalf("unexpected amount: %q", amount)
				}
				return nil
			},
		}
		svc := &Service{engine: eng, auth: auth}

		resp, err := svc.RepayAsset(ctx, req)
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if resp == nil {
			t.Fatalf("expected response")
		}
		if !auth.called {
			t.Fatalf("expected authorizer to be called")
		}
	})

	for _, tc := range sentinelErrorCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			auth := &fakeAuthorizer{}
			eng := &fakeEngine{
				repayFn: func(context.Context, string, string, string) error {
					return wrapError(tc.err)
				},
			}
			svc := &Service{engine: eng, auth: auth}

			_, err := svc.RepayAsset(ctx, req)
			if err == nil {
				t.Fatalf("expected error")
			}
			st := status.Convert(err)
			if st.Code() != tc.code {
				t.Fatalf("expected code %s, got %s", tc.code, st.Code())
			}
			if st.Message() != tc.msg {
				t.Fatalf("expected message %q, got %q", tc.msg, st.Message())
			}
			if !auth.called {
				t.Fatalf("expected authorizer to be called")
			}
		})
	}
}

func TestService_GetMarket(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		var capturedSymbol string
		eng := &fakeEngine{
			getMarketFn: func(_ context.Context, market string) (engine.Market, error) {
				capturedSymbol = market
				return engine.Market{
					Market: &engine.MarketSnapshot{
						PoolID:        "  nhb  ",
						SupplyIndex:   "  100  ",
						BorrowIndex:   "",
						ReserveFactor: 12,
					},
					RiskParameters: engine.RiskParameters{MaxLTV: 45},
				}, nil
			},
		}
		svc := &Service{engine: eng}

		resp, err := svc.GetMarket(ctx, &lendingv1.GetMarketRequest{Key: &lendingv1.MarketKey{Symbol: "  nhb  "}})
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if capturedSymbol != "nhb" {
			t.Fatalf("expected trimmed symbol, got %q", capturedSymbol)
		}
		if resp.GetMarket().GetKey().GetSymbol() != "nhb" {
			t.Fatalf("unexpected symbol: %q", resp.GetMarket().GetKey().GetSymbol())
		}
		if resp.GetMarket().GetLiquidityIndex() != "100" {
			t.Fatalf("unexpected liquidity index: %q", resp.GetMarket().GetLiquidityIndex())
		}
		if resp.GetMarket().GetBorrowIndex() != "0" {
			t.Fatalf("unexpected borrow index: %q", resp.GetMarket().GetBorrowIndex())
		}
		if resp.GetMarket().GetReserveFactor() != "12" {
			t.Fatalf("unexpected reserve factor: %q", resp.GetMarket().GetReserveFactor())
		}
		if resp.GetMarket().GetBaseAsset() != "NHB" {
			t.Fatalf("unexpected base asset: %q", resp.GetMarket().GetBaseAsset())
		}
		if resp.GetMarket().GetCollateralFactor() != "45" {
			t.Fatalf("unexpected collateral factor: %q", resp.GetMarket().GetCollateralFactor())
		}
	})

	t.Run("empty market", func(t *testing.T) {
		t.Parallel()

		svc := &Service{engine: &fakeEngine{getMarketFn: func(context.Context, string) (engine.Market, error) {
			return engine.Market{}, nil
		}}}

		resp, err := svc.GetMarket(ctx, &lendingv1.GetMarketRequest{})
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if resp.GetMarket() != nil {
			t.Fatalf("expected empty market response")
		}
	})

	for _, tc := range sentinelErrorCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &Service{engine: &fakeEngine{getMarketFn: func(context.Context, string) (engine.Market, error) {
				return engine.Market{}, wrapError(tc.err)
			}}}

			_, err := svc.GetMarket(ctx, &lendingv1.GetMarketRequest{})
			if err == nil {
				t.Fatalf("expected error")
			}
			st := status.Convert(err)
			if st.Code() != tc.code {
				t.Fatalf("expected code %s, got %s", tc.code, st.Code())
			}
			if st.Message() != tc.msg {
				t.Fatalf("expected message %q, got %q", tc.msg, st.Message())
			}
		})
	}
}

func TestService_ListMarkets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		svc := &Service{engine: &fakeEngine{listMarketsFn: func(context.Context) ([]engine.Market, error) {
			return []engine.Market{
				{Market: &engine.MarketSnapshot{PoolID: " nhb ", SupplyIndex: " 1 ", BorrowIndex: " 2 ", ReserveFactor: 5}, RiskParameters: engine.RiskParameters{MaxLTV: 60}},
				{Market: nil},
			}, nil
		}}}

		resp, err := svc.ListMarkets(ctx, &lendingv1.ListMarketsRequest{})
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if len(resp.GetMarkets()) != 1 {
			t.Fatalf("expected single market, got %d", len(resp.GetMarkets()))
		}
		market := resp.GetMarkets()[0]
		if market.GetKey().GetSymbol() != "nhb" {
			t.Fatalf("unexpected symbol: %q", market.GetKey().GetSymbol())
		}
		if market.GetLiquidityIndex() != "1" {
			t.Fatalf("unexpected liquidity index: %q", market.GetLiquidityIndex())
		}
		if market.GetBorrowIndex() != "2" {
			t.Fatalf("unexpected borrow index: %q", market.GetBorrowIndex())
		}
		if market.GetReserveFactor() != "5" {
			t.Fatalf("unexpected reserve factor: %q", market.GetReserveFactor())
		}
		if market.GetBaseAsset() != "NHB" {
			t.Fatalf("unexpected base asset: %q", market.GetBaseAsset())
		}
	})

	for _, tc := range sentinelErrorCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &Service{engine: &fakeEngine{listMarketsFn: func(context.Context) ([]engine.Market, error) {
				return nil, wrapError(tc.err)
			}}}

			_, err := svc.ListMarkets(ctx, &lendingv1.ListMarketsRequest{})
			if err == nil {
				t.Fatalf("expected error")
			}
			st := status.Convert(err)
			if st.Code() != tc.code {
				t.Fatalf("expected code %s, got %s", tc.code, st.Code())
			}
			if st.Message() != tc.msg {
				t.Fatalf("expected message %q, got %q", tc.msg, st.Message())
			}
		})
	}
}

func TestService_GetPosition(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	svc := &Service{engine: &fakeEngine{getPositionFn: func(context.Context, string, string) (engine.Position, error) {
		return engine.Position{}, nil
	}}}

	t.Run("nil request", func(t *testing.T) {
		t.Parallel()

		_, err := svc.GetPosition(ctx, nil)
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected invalid argument, got %v", err)
		}
	})

	t.Run("empty account", func(t *testing.T) {
		t.Parallel()

		_, err := svc.GetPosition(ctx, &lendingv1.GetPositionRequest{Account: "   "})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected invalid argument, got %v", err)
		}
	})

	t.Run("engine error", func(t *testing.T) {
		t.Parallel()

		svc := &Service{engine: &fakeEngine{getPositionFn: func(context.Context, string, string) (engine.Position, error) {
			return engine.Position{}, wrapError(engine.ErrPaused)
		}}}

		_, err := svc.GetPosition(ctx, &lendingv1.GetPositionRequest{Account: "alice"})
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("expected unavailable, got %v", err)
		}
	})

	t.Run("missing account snapshot", func(t *testing.T) {
		t.Parallel()

		svc := &Service{engine: &fakeEngine{getPositionFn: func(context.Context, string, string) (engine.Position, error) {
			return engine.Position{}, nil
		}}}

		_, err := svc.GetPosition(ctx, &lendingv1.GetPositionRequest{Account: "alice"})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("expected not found, got %v", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		var capturedAccount string
		svc := &Service{engine: &fakeEngine{getPositionFn: func(_ context.Context, account, market string) (engine.Position, error) {
			capturedAccount = account
			if market != "" {
				t.Fatalf("expected empty market filter")
			}
			return engine.Position{Account: &engine.AccountSnapshot{
				Address:        "  alice  ",
				SupplyShares:   "  10  ",
				DebtNHB:        "  2  ",
				CollateralZNHB: "  6  ",
			}}, nil
		}}}

		resp, err := svc.GetPosition(ctx, &lendingv1.GetPositionRequest{Account: "  alice  "})
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if capturedAccount != "alice" {
			t.Fatalf("expected trimmed account, got %q", capturedAccount)
		}
		pos := resp.GetPosition()
		if pos.GetAccount() != "alice" {
			t.Fatalf("unexpected account: %q", pos.GetAccount())
		}
		if pos.GetSupplied() != "10" {
			t.Fatalf("unexpected supplied amount: %q", pos.GetSupplied())
		}
		if pos.GetBorrowed() != "2" {
			t.Fatalf("unexpected borrowed amount: %q", pos.GetBorrowed())
		}
		if pos.GetCollateral() != "6" {
			t.Fatalf("unexpected collateral amount: %q", pos.GetCollateral())
		}
		if pos.GetHealthFactor() != "3" {
			t.Fatalf("unexpected health factor: %q", pos.GetHealthFactor())
		}
	})
}

func TestService_EnsureEngine(t *testing.T) {
	t.Parallel()

	if err := (&Service{}).ensureEngine(); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected failed precondition, got %v", err)
	}

	if err := (&Service{engine: &fakeEngine{}}).ensureEngine(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
