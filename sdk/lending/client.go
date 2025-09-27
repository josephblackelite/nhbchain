package lending

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	lendingv1 "nhbchain/proto/lending/v1"
)

// Client provides typed helpers over the Lending gRPC API.
type Client struct {
	conn *grpc.ClientConn
	raw  lendingv1.LendingServiceClient
}

// Dial connects to a lending service endpoint.
func Dial(ctx context.Context, target string, opts ...grpc.DialOption) (*Client, error) {
	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, err
	}
	return New(conn), nil
}

// New wraps an existing connection with typed helpers.
func New(conn *grpc.ClientConn) *Client {
	return &Client{
		conn: conn,
		raw:  lendingv1.NewLendingServiceClient(conn),
	}
}

// Close tears down the underlying connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Raw exposes the generated client for advanced interactions.
func (c *Client) Raw() lendingv1.LendingServiceClient {
	if c == nil {
		return nil
	}
	return c.raw
}

// GetMarket fetches a market definition by symbol.
func (c *Client) GetMarket(ctx context.Context, symbol string) (*lendingv1.Market, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.GetMarket(ctx, &lendingv1.GetMarketRequest{Key: &lendingv1.MarketKey{Symbol: symbol}})
	if err != nil {
		return nil, err
	}
	return resp.GetMarket(), nil
}

// ListMarkets enumerates all configured markets.
func (c *Client) ListMarkets(ctx context.Context) ([]*lendingv1.Market, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.ListMarkets(ctx, &lendingv1.ListMarketsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetMarkets(), nil
}

// GetPosition returns the account position summary.
func (c *Client) GetPosition(ctx context.Context, account string) (*lendingv1.AccountPosition, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.GetPosition(ctx, &lendingv1.GetPositionRequest{Account: account})
	if err != nil {
		return nil, err
	}
	return resp.GetPosition(), nil
}

// SupplyAsset submits a supply transaction and returns the updated position.
func (c *Client) SupplyAsset(ctx context.Context, account, symbol, amount string) (*lendingv1.AccountPosition, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.SupplyAsset(ctx, &lendingv1.SupplyAssetRequest{
		Account: account,
		Market:  &lendingv1.MarketKey{Symbol: symbol},
		Amount:  amount,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetPosition(), nil
}

// WithdrawAsset withdraws supplied collateral and returns the updated position.
func (c *Client) WithdrawAsset(ctx context.Context, account, symbol, amount string) (*lendingv1.AccountPosition, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.WithdrawAsset(ctx, &lendingv1.WithdrawAssetRequest{
		Account: account,
		Market:  &lendingv1.MarketKey{Symbol: symbol},
		Amount:  amount,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetPosition(), nil
}

// BorrowAsset executes a borrow against the supplied collateral.
func (c *Client) BorrowAsset(ctx context.Context, account, symbol, amount string) (*lendingv1.AccountPosition, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.BorrowAsset(ctx, &lendingv1.BorrowAssetRequest{
		Account: account,
		Market:  &lendingv1.MarketKey{Symbol: symbol},
		Amount:  amount,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetPosition(), nil
}

// RepayAsset repays borrowed balance and returns the latest position.
func (c *Client) RepayAsset(ctx context.Context, account, symbol, amount string) (*lendingv1.AccountPosition, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.RepayAsset(ctx, &lendingv1.RepayAssetRequest{
		Account: account,
		Market:  &lendingv1.MarketKey{Symbol: symbol},
		Amount:  amount,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetPosition(), nil
}
