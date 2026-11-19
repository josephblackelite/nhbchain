package network

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"nhbchain/crypto"
	"nhbchain/p2p"
	networkv1 "nhbchain/proto/network/v1"
)

var _ p2p.PeerMessageHandler = (*Relay)(nil)

type fixedPeer struct{ id string }

func (f fixedPeer) ID() string                 { return f.id }
func (f fixedPeer) Enqueue(*p2p.Message) error { return nil }

// attachedStream attaches a fresh stream state to the relay and returns it, so
// that what the relay forwards to the consensus service can be read back.
func attachedStream(r *Relay) *streamState {
	state := newStreamState(16)
	r.swapStream(state)
	return state
}

func nextEnvelope(t *testing.T, state *streamState) *networkv1.GossipMessage {
	t.Helper()
	select {
	case resp := <-state.queue:
		gossip := resp.GetEnvelope().GetGossip()
		if gossip == nil {
			t.Fatalf("expected a gossip envelope, got %v", resp)
		}
		return gossip
	case <-time.After(time.Second):
		t.Fatal("the relay forwarded nothing")
	}
	return nil
}

func TestRelayNamesTheSenderOfAChainDataRequestOnly(t *testing.T) {
	cases := []struct {
		name    string
		msgType byte
		tagged  bool
	}{
		{"get status", p2p.MsgTypeGetStatus, true},
		{"get blocks", p2p.MsgTypeGetBlocks, true},
		{"status", p2p.MsgTypeStatus, false},
		{"blocks", p2p.MsgTypeBlocks, false},
		{"block", p2p.MsgTypeBlock, false},
		{"transaction", p2p.MsgTypeTx, false},
		{"proposal", p2p.MsgTypeProposal, false},
		{"vote", p2p.MsgTypeVote, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relay := NewRelay()
			state := attachedStream(relay)

			if err := relay.HandlePeerMessage(fixedPeer{id: "peer-a"}, &p2p.Message{Type: tc.msgType, Payload: []byte(`{}`)}); err != nil {
				t.Fatalf("handle peer message: %v", err)
			}
			got := nextEnvelope(t, state)
			if got.Type != uint32(tc.msgType) || string(got.Payload) != `{}` {
				t.Fatalf("the message was altered in transit: %v", got)
			}
			if tc.tagged && got.PeerId != "peer-a" {
				t.Fatalf("expected the request to name its sender, got %q", got.PeerId)
			}
			if !tc.tagged && got.PeerId != "" {
				t.Fatalf("only a chain data request may name a peer, got %q", got.PeerId)
			}
		})
	}
}

func TestRelayDoesNotNameAPeerWithoutOne(t *testing.T) {
	relay := NewRelay()
	state := attachedStream(relay)

	request := &p2p.Message{Type: p2p.MsgTypeGetBlocks, Payload: []byte(`{}`)}
	if err := relay.HandlePeerMessage(nil, request); err != nil {
		t.Fatalf("handle peer message: %v", err)
	}
	if got := nextEnvelope(t, state); got.PeerId != "" {
		t.Fatalf("a request without a sender must not name a peer, got %q", got.PeerId)
	}
	if err := relay.HandleMessage(request); err != nil {
		t.Fatalf("handle message: %v", err)
	}
	if got := nextEnvelope(t, state); got.PeerId != "" {
		t.Fatalf("HandleMessage has no sender to name, got %q", got.PeerId)
	}
}

type collector struct {
	mu   sync.Mutex
	seen map[byte]int
}

func (c *collector) HandleMessage(msg *p2p.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = make(map[byte]int)
	}
	c.seen[msg.Type]++
	return nil
}

func (c *collector) count(msgType byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[msgType]
}

func startP2P(t *testing.T, handler p2p.MessageHandler) (*p2p.Server, string) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	server := p2p.NewServer(handler, key, p2p.ServerConfig{
		ListenAddress:    "127.0.0.1:0",
		ChainID:          1,
		GenesisHash:      bytes.Repeat([]byte{0x5A}, 32),
		ClientVersion:    "relay-test/1.0",
		MaxPeers:         8,
		MaxInbound:       8,
		MaxOutbound:      8,
		MaxMessageBytes:  1 << 20,
		RateMsgsPerSec:   1000,
		RateBurst:        1000,
		ReadTimeout:      30 * time.Second,
		WriteTimeout:     5 * time.Second,
		HandshakeTimeout: 3 * time.Second,
		PingInterval:     time.Hour,
	})
	go server.Start()
	t.Cleanup(func() { server.Stop() })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, addr := range server.ListenAddresses() {
			if !strings.HasSuffix(addr, ":0") {
				return server, addr
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server did not start listening")
	return nil, ""
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("timed out waiting for %s", what)
	}
}

// scriptedStream is the consensus side of a gossip stream: what the test puts
// on in is what the relay receives, and what the relay sends is read from out.
type scriptedStream struct {
	networkv1.NetworkService_GossipServer

	ctx context.Context
	in  chan *networkv1.GossipRequest
	out chan *networkv1.GossipResponse
}

func (s *scriptedStream) Context() context.Context { return s.ctx }

func (s *scriptedStream) Recv() (*networkv1.GossipRequest, error) {
	select {
	case req := <-s.in:
		return req, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func (s *scriptedStream) Send(resp *networkv1.GossipResponse) error {
	select {
	case s.out <- resp:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func gossipRequest(msgType byte, payload []byte, peerID string) *networkv1.GossipRequest {
	return &networkv1.GossipRequest{Envelope: &networkv1.NetworkEnvelope{
		Event: &networkv1.NetworkEnvelope_Gossip{Gossip: &networkv1.GossipMessage{
			Type:    uint32(msgType),
			Payload: payload,
			PeerId:  peerID,
		}},
	}}
}

func TestRelayDeliversAFrameThatNamesAPeerToThatPeerOnly(t *testing.T) {
	relay := NewRelay()
	hub, hubAddr := startP2P(t, relay)
	relay.SetServer(hub)

	asker, bystander := &collector{}, &collector{}
	askerServer, _ := startP2P(t, asker)
	bystanderServer, _ := startP2P(t, bystander)
	for _, peer := range []*p2p.Server{askerServer, bystanderServer} {
		if err := peer.Connect(hubAddr); err != nil {
			t.Fatalf("connect: %v", err)
		}
	}
	waitFor(t, 3*time.Second, "both peers to be connected", func() bool {
		return hub.SnapshotNetwork().Counts.Inbound == 2
	})
	askerID := askerServer.SnapshotNetwork().Self.NodeID
	bystanderID := bystanderServer.SnapshotNetwork().Self.NodeID
	if askerID == "" || bystanderID == "" || askerID == bystanderID {
		t.Fatalf("expected two distinct node ids, got %q and %q", askerID, bystanderID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream := &scriptedStream{ctx: ctx, in: make(chan *networkv1.GossipRequest, 8), out: make(chan *networkv1.GossipResponse, 16)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = relay.GossipStream(stream)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitFor(t, 3*time.Second, "the stream to attach", func() bool {
		relay.mu.RLock()
		defer relay.mu.RUnlock()
		return relay.stream != nil
	})

	// A request that arrives over the wire is forwarded together with the peer
	// it came from.
	getStatus, err := p2p.NewGetStatusMessage()
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	getBlocks, err := p2p.NewGetBlocksMessage(1)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := askerServer.Broadcast(getStatus); err != nil {
		t.Fatalf("send request: %v", err)
	}
	if err := bystanderServer.Broadcast(getBlocks); err != nil {
		t.Fatalf("send request: %v", err)
	}
	named := make(map[uint32]string)
	for i := 0; i < 2; i++ {
		select {
		case resp := <-stream.out:
			gossip := resp.GetEnvelope().GetGossip()
			named[gossip.GetType()] = gossip.GetPeerId()
		case <-time.After(3 * time.Second):
			t.Fatal("a request was not forwarded to the consensus service")
		}
	}
	if named[uint32(p2p.MsgTypeGetStatus)] != askerID || named[uint32(p2p.MsgTypeGetBlocks)] != bystanderID {
		t.Fatalf("requests were not tagged with the peer they came from: %v (asker %q, bystander %q)", named, askerID, bystanderID)
	}

	// A reply that names a peer reaches that peer and nobody else.
	status, err := p2p.NewStatusMessage(3)
	if err != nil {
		t.Fatalf("build status: %v", err)
	}
	stream.in <- gossipRequest(status.Type, status.Payload, askerID)
	waitFor(t, 3*time.Second, "the named peer to receive its reply", func() bool {
		return asker.count(p2p.MsgTypeStatus) == 1
	})
	time.Sleep(200 * time.Millisecond)
	if got := bystander.count(p2p.MsgTypeStatus); got != 0 {
		t.Fatalf("a reply for one peer was delivered to another: %d messages", got)
	}

	// A reply for a peer that is gone is dropped, not handed to the others.
	stream.in <- gossipRequest(status.Type, status.Payload, "not-a-connected-peer")
	// A frame that names nobody is still a broadcast. It is sent after the
	// frame above, so seeing it delivered also shows that one has been handled.
	stream.in <- gossipRequest(status.Type, status.Payload, "")
	waitFor(t, 3*time.Second, "the broadcast to reach both peers", func() bool {
		return asker.count(p2p.MsgTypeStatus) == 2 && bystander.count(p2p.MsgTypeStatus) == 1
	})
	time.Sleep(200 * time.Millisecond)
	if asker.count(p2p.MsgTypeStatus) != 2 || bystander.count(p2p.MsgTypeStatus) != 1 {
		t.Fatalf("expected exactly one delivery each of the broadcast and none of the reply for a missing peer, got asker=%d bystander=%d",
			asker.count(p2p.MsgTypeStatus), bystander.count(p2p.MsgTypeStatus))
	}
}

func TestClientSendToNeverBecomesABroadcast(t *testing.T) {
	c := &Client{sendCh: make(chan *networkv1.GossipRequest, 4)}
	msg := &p2p.Message{Type: p2p.MsgTypeStatus, Payload: []byte(`{}`)}

	if err := c.SendTo("", msg); err == nil {
		t.Fatal("a message without a peer must not be sent: it would reach every peer")
	}
	if len(c.sendCh) != 0 {
		t.Fatal("a message without a peer was queued")
	}

	if err := c.SendTo("peer-a", msg); err != nil {
		t.Fatalf("send to: %v", err)
	}
	if err := c.Broadcast(msg); err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	targeted := (<-c.sendCh).GetEnvelope().GetGossip()
	broadcast := (<-c.sendCh).GetEnvelope().GetGossip()
	if targeted.GetPeerId() != "peer-a" || targeted.GetType() != uint32(msg.Type) || string(targeted.GetPayload()) != `{}` {
		t.Fatalf("unexpected targeted frame: %v", targeted)
	}
	if broadcast.GetPeerId() != "" {
		t.Fatalf("a broadcast must not name a peer, got %q", broadcast.GetPeerId())
	}

	var notConnected Client
	if err := notConnected.SendTo("peer-a", msg); err == nil {
		t.Fatal("expected an error while the stream is not connected")
	}
}

// scriptedService is a p2pd that sends a fixed set of frames when a stream
// opens and reports what the client sends back.
type scriptedService struct {
	networkv1.UnimplementedNetworkServiceServer
	frames   []*networkv1.GossipMessage
	received chan *networkv1.GossipMessage
}

func (s *scriptedService) Gossip(stream networkv1.NetworkService_GossipServer) error {
	for _, frame := range s.frames {
		resp := &networkv1.GossipResponse{Envelope: &networkv1.NetworkEnvelope{
			Event: &networkv1.NetworkEnvelope_Gossip{Gossip: frame},
		}}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
	for {
		req, err := stream.Recv()
		if err != nil {
			return nil
		}
		if gossip := req.GetEnvelope().GetGossip(); gossip != nil {
			s.received <- gossip
		}
	}
}

func startScriptedService(t *testing.T, frames ...*networkv1.GossipMessage) (*scriptedService, *Client) {
	t.Helper()
	svc := &scriptedService{frames: frames, received: make(chan *networkv1.GossipMessage, 8)}
	server := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
	networkv1.RegisterNetworkServiceServer(server, svc)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := Dial(dialCtx, lis.Addr().String(), true)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return svc, client
}

type seenMessage struct {
	msgType byte
	from    string
	hasFrom bool
}

func TestClientRunPeersHandsTheHandlerTheRequestingPeer(t *testing.T) {
	svc, client := startScriptedService(t,
		&networkv1.GossipMessage{Type: uint32(p2p.MsgTypeGetStatus), Payload: []byte(`{}`), PeerId: "peer-a"},
		&networkv1.GossipMessage{Type: uint32(p2p.MsgTypeGetStatus), Payload: []byte(`{}`)},
	)

	var (
		mu   sync.Mutex
		seen []seenMessage
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- client.RunPeers(ctx, func(from p2p.PeerSender, msg *p2p.Message) error {
			entry := seenMessage{msgType: msg.Type}
			if from != nil {
				entry.hasFrom, entry.from = true, from.ID()
			}
			mu.Lock()
			seen = append(seen, entry)
			mu.Unlock()
			if from != nil {
				reply, err := p2p.NewStatusMessage(9)
				if err != nil {
					return err
				}
				return from.Enqueue(reply)
			}
			return nil
		}, nil)
	}()

	waitFor(t, 3*time.Second, "both requests to be handled", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 2
	})
	mu.Lock()
	first, second := seen[0], seen[1]
	mu.Unlock()
	if !first.hasFrom || first.from != "peer-a" {
		t.Fatalf("expected the tagged request to carry its peer, got %+v", first)
	}
	if second.hasFrom {
		t.Fatalf("expected an untagged request to carry no peer, got %+v", second)
	}

	select {
	case reply := <-svc.received:
		if reply.GetPeerId() != "peer-a" || reply.GetType() != uint32(p2p.MsgTypeStatus) {
			t.Fatalf("the answer must be addressed to the requesting peer, got %v", reply)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handler's answer never reached p2pd")
	}
	select {
	case extra := <-svc.received:
		t.Fatalf("only the tagged request should have been answered, got %v", extra)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	<-done
}

func TestClientRunStillDeliversEveryMessageToAPlainHandler(t *testing.T) {
	_, client := startScriptedService(t,
		&networkv1.GossipMessage{Type: uint32(p2p.MsgTypeGetBlocks), Payload: []byte(`{"from":1}`), PeerId: "peer-a"},
		&networkv1.GossipMessage{Type: uint32(p2p.MsgTypeTx), Payload: []byte(`{}`)},
	)

	var (
		mu    sync.Mutex
		types []byte
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx, func(msg *p2p.Message) error {
			mu.Lock()
			types = append(types, msg.Type)
			mu.Unlock()
			return nil
		}, nil)
	}()
	waitFor(t, 3*time.Second, "both messages to be handled", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(types) == 2
	})
	mu.Lock()
	got := append([]byte(nil), types...)
	mu.Unlock()
	if got[0] != p2p.MsgTypeGetBlocks || got[1] != p2p.MsgTypeTx {
		t.Fatalf("unexpected messages for a plain handler: %v", got)
	}
	cancel()
	<-done
}
