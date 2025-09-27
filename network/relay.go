package network

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"nhbchain/p2p"
	networkv1 "nhbchain/proto/network/v1"
)

const (
	streamQueueSize   = 128
	heartbeatInterval = 5 * time.Second
)

type streamState struct {
	queue chan *networkv1.GossipResponse
	done  chan struct{}
	once  sync.Once
}

func newStreamState() *streamState {
	return &streamState{
		queue: make(chan *networkv1.GossipResponse, streamQueueSize),
		done:  make(chan struct{}),
	}
}

func (s *streamState) close() {
	s.once.Do(func() {
		close(s.done)
		close(s.queue)
	})
}

// Relay bridges the P2P server with the consensus service over gRPC streams.
type Relay struct {
	mu     sync.RWMutex
	server *p2p.Server
	stream *streamState
}

// NewRelay constructs a Relay without any attached stream or server.
func NewRelay() *Relay {
	return &Relay{}
}

// SetServer attaches the backing P2P server. Unary RPCs and outbound broadcasts
// depend on this pointer.
func (r *Relay) SetServer(server *p2p.Server) {
	r.mu.Lock()
	r.server = server
	r.mu.Unlock()
}

// HandleMessage satisfies p2p.MessageHandler by forwarding gossip to the
// connected consensus stream. Messages are dropped if no stream is active.
func (r *Relay) HandleMessage(msg *p2p.Message) error {
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
	if r.enqueue(&networkv1.GossipResponse{Envelope: envelope}) {
		return nil
	}
	return errors.New("network relay: consensus stream unavailable")
}

// enqueue attempts to deliver the provided envelope to the active stream.
func (r *Relay) enqueue(envelope *networkv1.GossipResponse) bool {
	r.mu.RLock()
	stream := r.stream
	r.mu.RUnlock()
	if stream == nil {
		return false
	}
	select {
	case stream.queue <- envelope:
		return true
	default:
		// Stream is saturated; drop message to avoid blocking P2P read loop.
		return false
	}
}

// StartHeartbeats emits heartbeat envelopes at a fixed cadence until ctx is
// cancelled. Heartbeats are best-effort and silently dropped when no stream is
// attached.
func (r *Relay) StartHeartbeats(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = heartbeatInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			envelope := &networkv1.NetworkEnvelope{
				Event: &networkv1.NetworkEnvelope_Heartbeat{
					Heartbeat: &networkv1.Heartbeat{UnixMillis: t.UnixMilli()},
				},
			}
			_ = r.enqueue(&networkv1.GossipResponse{Envelope: envelope})
		}
	}
}

// GossipStream binds the supplied gRPC stream to the relay, multiplexing gossip
// and control messages between the P2P server and consensus service.
func (r *Relay) GossipStream(stream networkv1.NetworkService_GossipServer) error {
	if stream == nil {
		return fmt.Errorf("nil gossip stream")
	}
	state := newStreamState()
	r.swapStream(state)

	sendErr := make(chan error, 1)
	go func() {
		for envelope := range state.queue {
			if err := stream.Send(envelope); err != nil {
				sendErr <- err
				return
			}
		}
		sendErr <- nil
	}()

	defer func() {
		state.close()
		<-sendErr // drain sender goroutine result
	}()

	for {
		incoming, err := stream.Recv()
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				sendErr <- err
			}
			r.clearStream(state)
			return err
		}
		envelope := incoming.GetEnvelope()
		if envelope == nil {
			continue
		}
		switch event := envelope.Event.(type) {
		case *networkv1.NetworkEnvelope_Gossip:
			if event.Gossip == nil {
				continue
			}
			msg := &p2p.Message{
				Type:    byte(event.Gossip.Type),
				Payload: append([]byte(nil), event.Gossip.Payload...),
			}
			if err := r.broadcast(msg); err != nil {
				fmt.Printf("network relay broadcast failed: %v\n", err)
			}
		case *networkv1.NetworkEnvelope_Heartbeat:
			// Heartbeats flowing from consensus are ignored for now.
		default:
			fmt.Printf("network relay received unknown envelope: %T\n", event)
		}
	}
}

func (r *Relay) swapStream(state *streamState) {
	r.mu.Lock()
	if prev := r.stream; prev != nil {
		prev.close()
	}
	r.stream = state
	r.mu.Unlock()
}

func (r *Relay) clearStream(state *streamState) {
	r.mu.Lock()
	if r.stream == state {
		r.stream = nil
	}
	r.mu.Unlock()
	state.close()
}

func (r *Relay) broadcast(msg *p2p.Message) error {
	r.mu.RLock()
	server := r.server
	r.mu.RUnlock()
	if server == nil {
		return errors.New("network relay: broadcaster unavailable")
	}
	return server.Broadcast(msg)
}

// View exposes a snapshot of the current network view and listen addresses
// from the underlying server.
func (r *Relay) View() (p2p.NetworkView, []string, error) {
	r.mu.RLock()
	server := r.server
	r.mu.RUnlock()
	if server == nil {
		return p2p.NetworkView{}, nil, errors.New("p2p server unavailable")
	}
	view := server.SnapshotNetwork()
	listen := server.ListenAddresses()
	return view, listen, nil
}

// Peers exposes peer diagnostics from the underlying server.
func (r *Relay) Peers() ([]p2p.PeerNetInfo, error) {
	r.mu.RLock()
	server := r.server
	r.mu.RUnlock()
	if server == nil {
		return nil, errors.New("p2p server unavailable")
	}
	return server.NetPeers(), nil
}

// Dial requests the underlying server to dial the supplied target.
func (r *Relay) Dial(target string) error {
	r.mu.RLock()
	server := r.server
	r.mu.RUnlock()
	if server == nil {
		return errors.New("p2p server unavailable")
	}
	return server.DialPeer(target)
}

// Ban forwards ban requests to the server.
func (r *Relay) Ban(nodeID string, duration time.Duration) error {
	r.mu.RLock()
	server := r.server
	r.mu.RUnlock()
	if server == nil {
		return errors.New("p2p server unavailable")
	}
	return server.BanPeer(nodeID, duration)
}
