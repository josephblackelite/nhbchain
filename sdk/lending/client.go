package lending

import (
	"context"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	lendingv1 "nhbchain/proto/lending/v1"
	"nhbchain/sdk/internal/dial"
)

// DialOption configures the lending client dial behaviour.
type DialOption = dial.DialOption

var (
	// WithTransportCredentials configures the client to use the provided gRPC transport credentials.
	WithTransportCredentials = dial.WithTransportCredentials
	// WithTLSConfig configures the client to use the provided TLS configuration.
	WithTLSConfig = dial.WithTLSConfig
	// WithTLSFromFiles loads TLS credentials from certificate files.
	WithTLSFromFiles = dial.WithTLSFromFiles
	// WithSystemCertPool trusts the system certificate pool for TLS connections.
	WithSystemCertPool = dial.WithSystemCertPool
	// WithInsecure enables plaintext gRPC connections and should only be used for development.
	WithInsecure = dial.WithInsecure
	// WithContextDialer attaches a custom context-based dialer.
	WithContextDialer = dial.WithContextDialer
	// WithPerRPCCredentials attaches per-RPC credential authenticators.
	WithPerRPCCredentials = dial.WithPerRPCCredentials
	// WithDialOptions forwards arbitrary gRPC dial options to the connector.
	WithDialOptions = dial.WithDialOptions
)

// Client provides typed helpers over the Lending gRPC API.
type Client struct {
	conn *grpc.ClientConn
	raw  lendingv1.LendingServiceClient
}

// Dial connects to a lending service endpoint, defaulting to TLS when no explicit
// credentials are provided.
func Dial(ctx context.Context, target string, opts ...DialOption) (*Client, error) {
	dialOpts, err := dial.Resolve(opts...)
	if err != nil {
		return nil, err
	}
	dialOpts = append(dialOpts,
		grpc.WithChainUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithChainStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	conn, err := grpc.DialContext(ctx, target, dialOpts...)
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

// SupplyAsset relays the caller's already-signed supply transaction
// (signedTxJSON, JSON-encoded in the shape core/types.Transaction marshals
// to -- see NewSignedTx) and returns the mempool-accepted transaction hash.
// account/symbol/amount are for server-side validation/logging only; the
// signed transaction's own recovered signer is what actually authorizes the
// mutation on-chain. Poll GetPosition once the transaction confirms.
func (c *Client) SupplyAsset(ctx context.Context, account, symbol, amount, signedTxJSON string) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.raw.SupplyAsset(ctx, &lendingv1.SupplyAssetRequest{
		Account:      account,
		Market:       &lendingv1.MarketKey{Symbol: symbol},
		Amount:       amount,
		SignedTxJson: signedTxJSON,
	})
	if err != nil {
		return "", err
	}
	return resp.GetTxHash(), nil
}

// WithdrawAsset relays the caller's already-signed withdraw transaction. See SupplyAsset.
func (c *Client) WithdrawAsset(ctx context.Context, account, symbol, amount, signedTxJSON string) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.raw.WithdrawAsset(ctx, &lendingv1.WithdrawAssetRequest{
		Account:      account,
		Market:       &lendingv1.MarketKey{Symbol: symbol},
		Amount:       amount,
		SignedTxJson: signedTxJSON,
	})
	if err != nil {
		return "", err
	}
	return resp.GetTxHash(), nil
}

// BorrowAsset relays the caller's already-signed borrow transaction. See SupplyAsset.
func (c *Client) BorrowAsset(ctx context.Context, account, symbol, amount, signedTxJSON string) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.raw.BorrowAsset(ctx, &lendingv1.BorrowAssetRequest{
		Account:      account,
		Market:       &lendingv1.MarketKey{Symbol: symbol},
		Amount:       amount,
		SignedTxJson: signedTxJSON,
	})
	if err != nil {
		return "", err
	}
	return resp.GetTxHash(), nil
}

// RepayAsset relays the caller's already-signed repay transaction. See SupplyAsset.
func (c *Client) RepayAsset(ctx context.Context, account, symbol, amount, signedTxJSON string) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.raw.RepayAsset(ctx, &lendingv1.RepayAssetRequest{
		Account:      account,
		Market:       &lendingv1.MarketKey{Symbol: symbol},
		Amount:       amount,
		SignedTxJson: signedTxJSON,
	})
	if err != nil {
		return "", err
	}
	return resp.GetTxHash(), nil
}

// DepositCollateral relays the caller's already-signed ZNHB collateral deposit. See SupplyAsset.
func (c *Client) DepositCollateral(ctx context.Context, account, symbol, amount, signedTxJSON string) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.raw.DepositCollateral(ctx, &lendingv1.DepositCollateralRequest{
		Account:      account,
		Market:       &lendingv1.MarketKey{Symbol: symbol},
		Amount:       amount,
		SignedTxJson: signedTxJSON,
	})
	if err != nil {
		return "", err
	}
	return resp.GetTxHash(), nil
}

// WithdrawCollateral relays the caller's already-signed ZNHB collateral withdrawal. See SupplyAsset.
func (c *Client) WithdrawCollateral(ctx context.Context, account, symbol, amount, signedTxJSON string) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.raw.WithdrawCollateral(ctx, &lendingv1.WithdrawCollateralRequest{
		Account:      account,
		Market:       &lendingv1.MarketKey{Symbol: symbol},
		Amount:       amount,
		SignedTxJson: signedTxJSON,
	})
	if err != nil {
		return "", err
	}
	return resp.GetTxHash(), nil
}

// Liquidate relays a liquidator's already-signed transaction repaying an
// unhealthy borrower's debt. It is signed by the liquidator, not the
// borrower -- liquidation is a permissionless third-party action.
func (c *Client) Liquidate(ctx context.Context, liquidator, symbol, borrower, signedTxJSON string) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.raw.Liquidate(ctx, &lendingv1.LiquidateRequest{
		Liquidator:   liquidator,
		Market:       &lendingv1.MarketKey{Symbol: symbol},
		Borrower:     borrower,
		SignedTxJson: signedTxJSON,
	})
	if err != nil {
		return "", err
	}
	return resp.GetTxHash(), nil
}
