package client

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	lendingv1 "nhbchain/proto/lending/v1"
)

// Client provides a thin wrapper around the lending service gRPC API.
type Client struct {
	conn *grpc.ClientConn
	api  lendingv1.LendingServiceClient
}

// Dial initialises a client connection to the lending service endpoint.
func Dial(ctx context.Context, target string, opts ...grpc.DialOption) (*Client, error) {
	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, api: lendingv1.NewLendingServiceClient(conn)}, nil
}

// Close tears down the underlying connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Raw exposes the generated client for advanced usage.
func (c *Client) Raw() lendingv1.LendingServiceClient {
	if c == nil {
		return nil
	}
	return c.api
}
