package swap

import (
	"context"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	swapv1 "nhbchain/proto/swap/v1"
	"nhbchain/sdk/internal/dial"
)

// DialOption configures the swap client dial behaviour.
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

// Client exposes typed helpers for interacting with the swap service.
type Client struct {
	conn *grpc.ClientConn
	raw  swapv1.SwapServiceClient
}

// Dial connects to the swap service at the provided target address, defaulting
// to TLS credentials when no explicit transport configuration is supplied.
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
		raw:  swapv1.NewSwapServiceClient(conn),
	}
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Raw exposes the generated client for advanced usage.
func (c *Client) Raw() swapv1.SwapServiceClient {
	if c == nil {
		return nil
	}
	return c.raw
}

// GetPool fetches pool metadata for the provided pair.
func (c *Client) GetPool(ctx context.Context, base, quote string) (*swapv1.Pool, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.GetPool(ctx, &swapv1.GetPoolRequest{Key: &swapv1.PoolKey{Base: base, Quote: quote}})
	if err != nil {
		return nil, err
	}
	return resp.GetPool(), nil
}

// ListPools returns all available pools.
func (c *Client) ListPools(ctx context.Context) ([]*swapv1.Pool, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.ListPools(ctx, &swapv1.ListPoolsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetPools(), nil
}

// SwapExactIn quotes and executes a swap for a fixed input amount.
func (c *Client) SwapExactIn(ctx context.Context, base, quote, trader, amountIn, minOut string) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.raw.SwapExactIn(ctx, &swapv1.SwapExactInRequest{
		Key:          &swapv1.PoolKey{Base: base, Quote: quote},
		Trader:       trader,
		AmountIn:     amountIn,
		MinAmountOut: minOut,
	})
	if err != nil {
		return "", err
	}
	return resp.GetAmountOut(), nil
}

// SwapExactOut quotes and executes a swap targeting a fixed output amount.
func (c *Client) SwapExactOut(ctx context.Context, base, quote, trader, maxIn, amountOut string) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.raw.SwapExactOut(ctx, &swapv1.SwapExactOutRequest{
		Key:         &swapv1.PoolKey{Base: base, Quote: quote},
		Trader:      trader,
		MaxAmountIn: maxIn,
		AmountOut:   amountOut,
	})
	if err != nil {
		return "", err
	}
	return resp.GetAmountIn(), nil
}
