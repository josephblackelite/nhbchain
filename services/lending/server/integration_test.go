package server

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	lendingv1 "nhbchain/proto/lending/v1"
	"nhbchain/services/lending/engine"
)

const bufSize = 1024 * 1024

func TestIntegration_LendingService(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	var (
		supplyCalled   bool
		withdrawCalled bool
		borrowCalled   bool
		repayCalled    bool
	)

	eng := &fakeEngine{
		getMarketFn: func(_ context.Context, market string) (engine.Market, error) {
			if market != "nhb" {
				t.Fatalf("unexpected market lookup: %q", market)
			}
			return engine.Market{
				Market: &engine.MarketSnapshot{
					PoolID:        "  nhb  ",
					SupplyIndex:   " 1 ",
					BorrowIndex:   " 2 ",
					ReserveFactor: 250,
				},
				RiskParameters: engine.RiskParameters{MaxLTV: 7500},
			}, nil
		},
		listMarketsFn: func(context.Context) ([]engine.Market, error) {
			return []engine.Market{
				{
					Market: &engine.MarketSnapshot{
						PoolID:        "  nhb  ",
						SupplyIndex:   " 10 ",
						BorrowIndex:   " 20 ",
						ReserveFactor: 150,
					},
					RiskParameters: engine.RiskParameters{MaxLTV: 5000},
				},
				{},
			}, nil
		},
		getPositionFn: func(_ context.Context, addr, market string) (engine.Position, error) {
			if addr != "charlie" {
				t.Fatalf("unexpected account lookup: %q", addr)
			}
			if market != "" {
				t.Fatalf("unexpected market filter: %q", market)
			}
			return engine.Position{
				Account: &engine.AccountSnapshot{
					Address:        "  charlie  ",
					SupplyShares:   " 100 ",
					DebtNHB:        " 50 ",
					CollateralZNHB: " 150 ",
				},
			}, nil
		},
		supplyFn: func(_ context.Context, addr, market, amount string) error {
			supplyCalled = true
			if addr != "alice" {
				t.Fatalf("unexpected supply account: %q", addr)
			}
			if market != "nhb" {
				t.Fatalf("unexpected supply market: %q", market)
			}
			if amount != "1000" {
				t.Fatalf("unexpected supply amount: %q", amount)
			}
			return nil
		},
		withdrawFn: func(_ context.Context, addr, market, amount string) error {
			withdrawCalled = true
			if addr != "bob" {
				t.Fatalf("unexpected withdraw account: %q", addr)
			}
			if market != "usdc" {
				t.Fatalf("unexpected withdraw market: %q", market)
			}
			if amount != "250" {
				t.Fatalf("unexpected withdraw amount: %q", amount)
			}
			return nil
		},
		borrowFn: func(_ context.Context, addr, market, amount string) error {
			borrowCalled = true
			if addr != "dave" {
				t.Fatalf("unexpected borrow account: %q", addr)
			}
			if market != "eth" {
				t.Fatalf("unexpected borrow market: %q", market)
			}
			if amount != "75" {
				t.Fatalf("unexpected borrow amount: %q", amount)
			}
			return nil
		},
		repayFn: func(_ context.Context, addr, market, amount string) error {
			repayCalled = true
			if addr != "erin" {
				t.Fatalf("unexpected repay account: %q", addr)
			}
			if market != "btc" {
				t.Fatalf("unexpected repay market: %q", market)
			}
			if amount != "125" {
				t.Fatalf("unexpected repay amount: %q", amount)
			}
			return nil
		},
	}

	listener := bufconn.Listen(bufSize)
	t.Cleanup(func() {
		listener.Close()
	})

	grpcServer := grpc.NewServer()
	lendingv1.RegisterLendingServiceServer(grpcServer, New(eng, nil, nil))
	reflection.Register(grpcServer)

	go func() {
		if err := grpcServer.Serve(listener); err != nil && err != grpc.ErrServerStopped {
			t.Errorf("serve bufconn: %v", err)
		}
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
	})

	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.Dial()
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
	})

	client := lendingv1.NewLendingServiceClient(conn)

	t.Run("GetMarket", func(t *testing.T) {
		resp, err := client.GetMarket(ctx, &lendingv1.GetMarketRequest{Key: &lendingv1.MarketKey{Symbol: "  nhb  "}})
		if status.Code(err) != codes.OK {
			t.Fatalf("expected OK, got %v", err)
		}
		if resp == nil || resp.GetMarket() == nil {
			t.Fatalf("expected market response")
		}
		market := resp.GetMarket()
		if market.GetKey().GetSymbol() != "nhb" {
			t.Fatalf("expected market symbol to be trimmed, got %q", market.GetKey().GetSymbol())
		}
		if market.GetBaseAsset() != "NHB" {
			t.Fatalf("expected base asset NHB, got %q", market.GetBaseAsset())
		}
		if market.GetCollateralFactor() != "7500" {
			t.Fatalf("unexpected collateral factor: %q", market.GetCollateralFactor())
		}
		if market.GetLiquidityIndex() != "1" || market.GetBorrowIndex() != "2" {
			t.Fatalf("unexpected index values: %q / %q", market.GetLiquidityIndex(), market.GetBorrowIndex())
		}
	})

	t.Run("ListMarkets", func(t *testing.T) {
		resp, err := client.ListMarkets(ctx, &lendingv1.ListMarketsRequest{})
		if status.Code(err) != codes.OK {
			t.Fatalf("expected OK, got %v", err)
		}
		markets := resp.GetMarkets()
		if len(markets) != 1 {
			t.Fatalf("expected single market entry, got %d", len(markets))
		}
		if markets[0].GetKey().GetSymbol() != "nhb" {
			t.Fatalf("unexpected market symbol: %q", markets[0].GetKey().GetSymbol())
		}
		if markets[0].GetCollateralFactor() != "5000" {
			t.Fatalf("unexpected collateral factor: %q", markets[0].GetCollateralFactor())
		}
	})

	t.Run("GetPosition", func(t *testing.T) {
		resp, err := client.GetPosition(ctx, &lendingv1.GetPositionRequest{Account: "  charlie  "})
		if status.Code(err) != codes.OK {
			t.Fatalf("expected OK, got %v", err)
		}
		position := resp.GetPosition()
		if position.GetAccount() != "charlie" {
			t.Fatalf("expected trimmed account, got %q", position.GetAccount())
		}
		if position.GetSupplied() != "100" {
			t.Fatalf("unexpected supplied amount: %q", position.GetSupplied())
		}
		if position.GetBorrowed() != "50" {
			t.Fatalf("unexpected borrowed amount: %q", position.GetBorrowed())
		}
		if position.GetCollateral() != "150" {
			t.Fatalf("unexpected collateral amount: %q", position.GetCollateral())
		}
		if position.GetHealthFactor() != "3" {
			t.Fatalf("unexpected health factor: %q", position.GetHealthFactor())
		}
	})

	t.Run("SupplyAsset", func(t *testing.T) {
		_, err := client.SupplyAsset(ctx, &lendingv1.SupplyAssetRequest{
			Account: "  alice  ",
			Market:  &lendingv1.MarketKey{Symbol: "  nhb  "},
			Amount:  " 1000 ",
		})
		if status.Code(err) != codes.OK {
			t.Fatalf("expected OK, got %v", err)
		}
		if !supplyCalled {
			t.Fatalf("expected supply to be invoked")
		}
	})

	t.Run("WithdrawAsset", func(t *testing.T) {
		_, err := client.WithdrawAsset(ctx, &lendingv1.WithdrawAssetRequest{
			Account: "  bob  ",
			Market:  &lendingv1.MarketKey{Symbol: "  usdc  "},
			Amount:  " 250 ",
		})
		if status.Code(err) != codes.OK {
			t.Fatalf("expected OK, got %v", err)
		}
		if !withdrawCalled {
			t.Fatalf("expected withdraw to be invoked")
		}
	})

	t.Run("BorrowAsset", func(t *testing.T) {
		_, err := client.BorrowAsset(ctx, &lendingv1.BorrowAssetRequest{
			Account: "  dave  ",
			Market:  &lendingv1.MarketKey{Symbol: "  eth  "},
			Amount:  " 75 ",
		})
		if status.Code(err) != codes.OK {
			t.Fatalf("expected OK, got %v", err)
		}
		if !borrowCalled {
			t.Fatalf("expected borrow to be invoked")
		}
	})

	t.Run("RepayAsset", func(t *testing.T) {
		_, err := client.RepayAsset(ctx, &lendingv1.RepayAssetRequest{
			Account: "  erin  ",
			Market:  &lendingv1.MarketKey{Symbol: "  btc  "},
			Amount:  " 125 ",
		})
		if status.Code(err) != codes.OK {
			t.Fatalf("expected OK, got %v", err)
		}
		if !repayCalled {
			t.Fatalf("expected repay to be invoked")
		}
	})
}
