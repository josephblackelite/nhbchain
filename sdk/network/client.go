package network

import (
	"context"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	networkv1 "nhbchain/proto/network/v1"
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

// Client is a thin wrapper around the generated Network gRPC client.
type Client struct {
	conn *grpc.ClientConn
	raw  networkv1.NetworkServiceClient
}

// Dial initialises a client connection to the network daemon. When no dial
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

// New wraps an existing connection in the typed client.
func New(conn *grpc.ClientConn) *Client {
	return &Client{
		conn: conn,
		raw:  networkv1.NewNetworkServiceClient(conn),
	}
}

// Close tears down the underlying connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Raw exposes the generated client.
func (c *Client) Raw() networkv1.NetworkServiceClient {
	if c == nil {
		return nil
	}
	return c.raw
}

// Gossip opens the bidirectional gossip stream.
func (c *Client) Gossip(ctx context.Context) (*GossipStream, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	stream, err := c.raw.Gossip(ctx)
	if err != nil {
		return nil, err
	}
	return &GossipStream{stream: stream}, nil
}

// GetView fetches diagnostic information about the network service.
func (c *Client) GetView(ctx context.Context) (*networkv1.NetworkView, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.GetView(ctx, &networkv1.GetViewRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetView(), nil
}

// ListPeers retrieves peer metadata currently tracked by the daemon.
func (c *Client) ListPeers(ctx context.Context) ([]*networkv1.PeerNetInfo, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.ListPeers(ctx, &networkv1.ListPeersRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetPeers(), nil
}

// DialPeer instructs the daemon to connect to the provided target address.
func (c *Client) DialPeer(ctx context.Context, target string) error {
	if c == nil {
		return grpc.ErrClientConnClosing
	}
	_, err := c.raw.DialPeer(ctx, &networkv1.DialPeerRequest{Target: target})
	return err
}

// BanPeer applies a temporary ban to the given node identifier.
func (c *Client) BanPeer(ctx context.Context, nodeID string, seconds int64) error {
	if c == nil {
		return grpc.ErrClientConnClosing
	}
	_, err := c.raw.BanPeer(ctx, &networkv1.BanPeerRequest{NodeId: nodeID, Seconds: seconds})
	return err
}

// GossipStream exposes helpers for working with the bidirectional stream.
type GossipStream struct {
	stream networkv1.NetworkService_GossipClient
}

// Send transmits a gossip request to the daemon.
func (s *GossipStream) Send(request *networkv1.GossipRequest) error {
	if s == nil {
		return grpc.ErrClientConnClosing
	}
	return s.stream.Send(request)
}

// Recv blocks until the next envelope is received.
func (s *GossipStream) Recv() (*networkv1.GossipResponse, error) {
	if s == nil {
		return nil, grpc.ErrClientConnClosing
	}
	return s.stream.Recv()
}

// CloseSend signals that no further messages will be sent on the stream.
func (s *GossipStream) CloseSend() error {
	if s == nil {
		return nil
	}
	return s.stream.CloseSend()
}
