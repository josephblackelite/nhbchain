package consensus

import (
	"context"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	consensusv1 "nhbchain/proto/consensus/v1"
	"nhbchain/sdk/internal/dial"
)

// DialOption configures the underlying gRPC dial behaviour.
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

// Client wraps the generated gRPC client with typed helpers.
type Client struct {
	conn  *grpc.ClientConn
	raw   consensusv1.ConsensusServiceClient
	query consensusv1.QueryServiceClient
}

// Dial initialises a consensus client for the provided target. When no dial
// options are provided the connector defaults to TLS credentials using the host
// certificate pool.
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

// New builds a client wrapper from an existing gRPC connection.
func New(conn *grpc.ClientConn) *Client {
	return &Client{
		conn:  conn,
		raw:   consensusv1.NewConsensusServiceClient(conn),
		query: consensusv1.NewQueryServiceClient(conn),
	}
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Raw exposes the underlying generated client for advanced interactions.
func (c *Client) Raw() consensusv1.ConsensusServiceClient {
	if c == nil {
		return nil
	}
	return c.raw
}

// QueryClient exposes the generated query service client.
func (c *Client) QueryClient() consensusv1.QueryServiceClient {
	if c == nil {
		return nil
	}
	return c.query
}

// SubmitTransaction pushes a transaction into the validator mempool.
func (c *Client) SubmitTransaction(ctx context.Context, tx *consensusv1.Transaction) error {
	if c == nil {
		return grpc.ErrClientConnClosing
	}
	_, err := c.raw.SubmitTransaction(ctx, &consensusv1.SubmitTransactionRequest{Transaction: tx})
	return err
}

// SubmitEnvelope pushes a signed transaction envelope into the validator mempool.
func (c *Client) SubmitEnvelope(ctx context.Context, tx *consensusv1.SignedTxEnvelope) error {
	if c == nil {
		return grpc.ErrClientConnClosing
	}
	_, err := c.raw.SubmitTxEnvelope(ctx, &consensusv1.SubmitTxEnvelopeRequest{Tx: tx})
	return err
}

// GetHeight fetches the chain height tracked by the validator.
func (c *Client) GetHeight(ctx context.Context) (uint64, error) {
	if c == nil {
		return 0, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.GetHeight(ctx, &consensusv1.GetHeightRequest{})
	if err != nil {
		return 0, err
	}
	return resp.GetHeight(), nil
}

// GetBlockByHeight returns the block at the provided height if available.
func (c *Client) GetBlockByHeight(ctx context.Context, height uint64) (*consensusv1.Block, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.GetBlockByHeight(ctx, &consensusv1.GetBlockByHeightRequest{Height: height})
	if err != nil {
		return nil, err
	}
	return resp.GetBlock(), nil
}

// GetValidatorSet fetches the current validator map.
func (c *Client) GetValidatorSet(ctx context.Context) ([]*consensusv1.Validator, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.GetValidatorSet(ctx, &consensusv1.GetValidatorSetRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetValidators(), nil
}

// GetMempool returns the current pending transactions held by the validator.
func (c *Client) GetMempool(ctx context.Context) ([]*consensusv1.Transaction, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.GetMempool(ctx, &consensusv1.GetMempoolRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetTransactions(), nil
}
