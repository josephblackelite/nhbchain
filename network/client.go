package network

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"nhbchain/p2p"
	networkv1 "nhbchain/proto/network/v1"
)

var (
	errNotConnected   = errors.New("network client not connected")
	errQueueSaturated = errors.New("network client send queue full")
)

// Client maintains the bidirectional stream with p2pd and implements
// p2p.Broadcaster for consensus components.
type Client struct {
	conn   *grpc.ClientConn
	client networkv1.NetworkServiceClient

	mu     sync.RWMutex
	sendCh chan *networkv1.GossipRequest
}

// Dial initialises a network client against the provided address. TLS is used
// by default unless the caller explicitly allows plaintext connections.
func Dial(ctx context.Context, target string, allowInsecure bool, opts ...grpc.DialOption) (*Client, error) {
	if len(opts) == 0 {
		if allowInsecure {
			opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		} else {
			opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})))
		}
	} else {
		if allowInsecure {
			opts = append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, opts...)
		} else {
			opts = append([]grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12}))}, opts...)
		}
	}
	opts = append(opts,
		grpc.WithChainUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithChainStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, err
	}
	return NewClient(conn), nil
}

// NewClient wraps an existing gRPC connection.
func NewClient(conn *grpc.ClientConn) *Client {
	return &Client{
		conn:   conn,
		client: networkv1.NewNetworkServiceClient(conn),
	}
}

// Close tears down the underlying gRPC connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Broadcast implements p2p.Broadcaster by enqueueing the message onto the gRPC
// stream. The call is non-blocking and drops when the queue is saturated.
func (c *Client) Broadcast(msg *p2p.Message) error {
	if msg == nil {
		return nil
	}
	envelope := &networkv1.NetworkEnvelope{
		Event: &networkv1.NetworkEnvelope_Gossip{
			Gossip: &networkv1.GossipMessage{
				Type:    uint32(msg.Type),
				Payload: append([]byte(nil), msg.Payload...),
			},
		},
	}
	c.mu.RLock()
	ch := c.sendCh
	c.mu.RUnlock()
	if ch == nil {
		return errNotConnected
	}
	select {
	case ch <- &networkv1.GossipRequest{Envelope: envelope}:
		return nil
	default:
		return errQueueSaturated
	}
}

// Run establishes the streaming RPC and continuously processes inbound events
// until the context is cancelled or the stream terminates. Gossip payloads are
// forwarded to handleMessage while heartbeats trigger handleHeartbeat when
// provided.
func (c *Client) Run(ctx context.Context, handleMessage func(*p2p.Message) error, handleHeartbeat func(time.Time)) error {
	if c == nil {
		return fmt.Errorf("nil network client")
	}
	stream, err := c.client.Gossip(ctx)
	if err != nil {
		return err
	}

	sendCh := make(chan *networkv1.GossipRequest, streamQueueSize)
	c.mu.Lock()
	c.sendCh = sendCh
	c.mu.Unlock()

	sendErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				sendErr <- ctx.Err()
				return
			case envelope, ok := <-sendCh:
				if !ok {
					sendErr <- nil
					return
				}
				if err := stream.Send(envelope); err != nil {
					sendErr <- err
					return
				}
			}
		}
	}()

	defer func() {
		c.mu.Lock()
		c.sendCh = nil
		c.mu.Unlock()
		close(sendCh)
		<-sendErr
	}()

	for {
		response, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		envelope := response.GetEnvelope()
		if envelope == nil {
			continue
		}
		switch event := envelope.Event.(type) {
		case *networkv1.NetworkEnvelope_Gossip:
			if event.Gossip == nil || handleMessage == nil {
				continue
			}
			msg := &p2p.Message{
				Type:    byte(event.Gossip.Type),
				Payload: append([]byte(nil), event.Gossip.Payload...),
			}
			if err := handleMessage(msg); err != nil {
				// TODO: determine whether repeated handler failures should trigger
				// peer backoff or stream termination instead of continuing.
				fmt.Printf("network client handler error: %v\n", err)
			}
		case *networkv1.NetworkEnvelope_Heartbeat:
			if handleHeartbeat == nil || event.Heartbeat == nil {
				continue
			}
			ts := time.UnixMilli(event.Heartbeat.UnixMillis)
			handleHeartbeat(ts)
		default:
			// Unknown events are ignored.
		}
	}
}

// NetworkView queries the p2p daemon for network statistics and listen
// addresses.
func (c *Client) NetworkView(ctx context.Context) (p2p.NetworkView, []string, error) {
	if c == nil {
		return p2p.NetworkView{}, nil, fmt.Errorf("nil network client")
	}
	resp, err := c.client.GetView(ctx, &networkv1.GetViewRequest{})
	if err != nil {
		return p2p.NetworkView{}, nil, err
	}
	return decodeView(resp.GetView())
}

// NetworkPeers fetches peer diagnostics from p2pd.
func (c *Client) NetworkPeers(ctx context.Context) ([]p2p.PeerNetInfo, error) {
	if c == nil {
		return nil, fmt.Errorf("nil network client")
	}
	resp, err := c.client.ListPeers(ctx, &networkv1.ListPeersRequest{})
	if err != nil {
		return nil, err
	}
	peers := resp.GetPeers()
	infos := make([]p2p.PeerNetInfo, 0, len(peers))
	for _, peer := range peers {
		infos = append(infos, decodePeerNetInfo(peer))
	}
	return infos, nil
}

// Dial forwards a dial request to p2pd.
func (c *Client) Dial(ctx context.Context, target string) error {
	if c == nil {
		return fmt.Errorf("nil network client")
	}
	_, err := c.client.DialPeer(ctx, &networkv1.DialPeerRequest{Target: target})
	return err
}

// Ban instructs p2pd to ban a peer for the provided duration.
func (c *Client) Ban(ctx context.Context, nodeID string, duration time.Duration) error {
	if c == nil {
		return fmt.Errorf("nil network client")
	}
	seconds := int64(duration / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	_, err := c.client.BanPeer(ctx, &networkv1.BanPeerRequest{NodeId: nodeID, Seconds: seconds})
	return err
}

func decodeView(view *networkv1.NetworkView) (p2p.NetworkView, []string, error) {
	if view == nil {
		return p2p.NetworkView{}, nil, fmt.Errorf("network view missing")
	}
	result := p2p.NetworkView{
		NetworkID: view.GetNetworkId(),
		Genesis:   string(view.GetGenesisHash()),
	}
	if counts := view.GetCounts(); counts != nil {
		result.Counts = p2p.NetworkCounts{
			Total:    int(counts.GetTotal()),
			Inbound:  int(counts.GetInbound()),
			Outbound: int(counts.GetOutbound()),
		}
	}
	if limits := view.GetLimits(); limits != nil {
		result.Limits = p2p.NetworkLimits{
			MaxPeers:    int(limits.GetMaxPeers()),
			MaxInbound:  int(limits.GetMaxInbound()),
			MaxOutbound: int(limits.GetMaxOutbound()),
			Rate:        limits.GetRateMsgsPerSec(),
			Burst:       limits.GetBurst(),
			BanScore:    int(limits.GetBanScore()),
			GreyScore:   int(limits.GetGreyScore()),
		}
	}
	if self := view.GetSelf(); self != nil {
		result.Self = p2p.NetworkSelf{
			NodeID:          self.GetNodeId(),
			ProtocolVersion: self.GetProtocolVersion(),
			ClientVersion:   self.GetClientVersion(),
		}
	}
	result.Bootnodes = append([]string{}, view.GetBootnodes()...)
	result.Persistent = append([]string{}, view.GetPersistentPeers()...)
	seeds := view.GetSeeds()
	result.Seeds = make([]p2p.SeedInfo, 0, len(seeds))
	for _, seed := range seeds {
		if seed == nil {
			continue
		}
		result.Seeds = append(result.Seeds, p2p.SeedInfo{
			NodeID:    seed.GetNodeId(),
			Address:   seed.GetAddress(),
			Source:    seed.GetSource(),
			NotBefore: seed.GetNotBefore(),
			NotAfter:  seed.GetNotAfter(),
		})
	}
	listen := append([]string{}, view.GetListenAddrs()...)
	return result, listen, nil
}

func decodePeerNetInfo(info *networkv1.PeerNetInfo) p2p.PeerNetInfo {
	if info == nil {
		return p2p.PeerNetInfo{}
	}
	lastSeen := time.Unix(info.GetLastSeenUnix(), 0)
	banned := time.Unix(info.GetBannedUntilUnix(), 0)
	if info.GetBannedUntilUnix() == 0 {
		banned = time.Time{}
	}
	return p2p.PeerNetInfo{
		NodeID:      info.GetNodeId(),
		Address:     info.GetAddress(),
		Direction:   info.GetDirection(),
		State:       info.GetState(),
		Score:       int(info.GetScore()),
		LastSeen:    lastSeen,
		Fails:       int(info.GetFails()),
		BannedUntil: banned,
	}
}
