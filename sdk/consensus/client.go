package consensus

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	consensusv1 "nhbchain/proto/consensus/v1"
)

// Client wraps the generated gRPC client with typed helpers.
type Client struct {
	conn  *grpc.ClientConn
	raw   consensusv1.ConsensusServiceClient
	query consensusv1.QueryServiceClient
}

// Dial initialises a consensus client for the provided target. If no dial options
// are supplied the connection defaults to insecure transport credentials.
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
