package client

import (
	"context"
	"fmt"
	"math/big"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"nhbchain/consensus/codec"
	"nhbchain/core/types"
	consensuspb "nhbchain/proto/consensus"
)

// Client is a convenience wrapper around the generated gRPC client.
type Client struct {
	conn *grpc.ClientConn
	svc  consensuspb.ConsensusClient
}

// Dial initialises a consensus client against the provided target.
func Dial(ctx context.Context, target string, opts ...grpc.DialOption) (*Client, error) {
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}
	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, svc: consensuspb.NewConsensusClient(conn)}, nil
}

// Close tears down the client connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// SubmitTransaction pushes a transaction into the remote mempool.
func (c *Client) SubmitTransaction(ctx context.Context, tx *types.Transaction) error {
	if c == nil {
		return fmt.Errorf("consensus client not initialised")
	}
	msg, err := codec.TransactionToProto(tx)
	if err != nil {
		return err
	}
	_, err = c.svc.SubmitTransaction(ctx, &consensuspb.SubmitTransactionRequest{Transaction: msg})
	return err
}

// GetValidatorSet fetches the validator map from the remote node.
func (c *Client) GetValidatorSet(ctx context.Context) (map[string]*big.Int, error) {
	if c == nil {
		return nil, fmt.Errorf("consensus client not initialised")
	}
	resp, err := c.svc.GetValidatorSet(ctx, &consensuspb.GetValidatorSetRequest{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]*big.Int, len(resp.GetValidators()))
	for _, validator := range resp.GetValidators() {
		power := new(big.Int)
		if validator.GetPower() != "" {
			if _, ok := power.SetString(validator.GetPower(), 10); !ok {
				return nil, fmt.Errorf("invalid validator power %q", validator.GetPower())
			}
		} else {
			power = nil
		}
		out[string(append([]byte(nil), validator.GetAddress()...))] = power
	}
	return out, nil
}

// GetBlockByHeight retrieves a block from the remote node.
func (c *Client) GetBlockByHeight(ctx context.Context, height uint64) (*types.Block, error) {
	if c == nil {
		return nil, fmt.Errorf("consensus client not initialised")
	}
	resp, err := c.svc.GetBlockByHeight(ctx, &consensuspb.GetBlockByHeightRequest{Height: height})
	if err != nil {
		return nil, err
	}
	return codec.BlockFromProto(resp.GetBlock())
}

// GetHeight fetches the current height.
func (c *Client) GetHeight(ctx context.Context) (uint64, error) {
	if c == nil {
		return 0, fmt.Errorf("consensus client not initialised")
	}
	resp, err := c.svc.GetHeight(ctx, &consensuspb.GetHeightRequest{})
	if err != nil {
		return 0, err
	}
	return resp.GetHeight(), nil
}

// GetMempool retrieves transactions awaiting inclusion.
func (c *Client) GetMempool(ctx context.Context) ([]*types.Transaction, error) {
	if c == nil {
		return nil, fmt.Errorf("consensus client not initialised")
	}
	resp, err := c.svc.GetMempool(ctx, &consensuspb.GetMempoolRequest{})
	if err != nil {
		return nil, err
	}
	return codec.TransactionsFromProto(resp.GetTransactions())
}

// CreateBlock asks the remote node to assemble a block from provided transactions.
func (c *Client) CreateBlock(ctx context.Context, txs []*types.Transaction) (*types.Block, error) {
	if c == nil {
		return nil, fmt.Errorf("consensus client not initialised")
	}
	protoTxs, err := codec.TransactionsToProto(txs)
	if err != nil {
		return nil, err
	}
	resp, err := c.svc.CreateBlock(ctx, &consensuspb.CreateBlockRequest{Transactions: protoTxs})
	if err != nil {
		return nil, err
	}
	return codec.BlockFromProto(resp.GetBlock())
}

// CommitBlock submits a block to be committed on the remote node.
func (c *Client) CommitBlock(ctx context.Context, block *types.Block) error {
	if c == nil {
		return fmt.Errorf("consensus client not initialised")
	}
	msg, err := codec.BlockToProto(block)
	if err != nil {
		return err
	}
	_, err = c.svc.CommitBlock(ctx, &consensuspb.CommitBlockRequest{Block: msg})
	return err
}

// GetLastCommitHash fetches the proposer selection seed.
func (c *Client) GetLastCommitHash(ctx context.Context) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("consensus client not initialised")
	}
	resp, err := c.svc.GetLastCommitHash(ctx, &consensuspb.GetLastCommitHashRequest{})
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), resp.GetHash()...), nil
}
