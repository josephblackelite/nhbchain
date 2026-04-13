package network

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"nhbchain/p2p"
	networkv1 "nhbchain/proto/network/v1"
)

const (
	// DefaultStreamQueueSize controls the buffered size for relay queues when no
	// override is supplied via configuration.
	DefaultStreamQueueSize = 128
	heartbeatInterval      = 5 * time.Second

	defaultDropAlertRatio = 0.1
	dropLogCooldown       = 30 * time.Second
)

type streamState struct {
	queue chan *networkv1.GossipResponse
	done  chan struct{}
	once  sync.Once
}

func newStreamState(size int) *streamState {
	if size <= 0 {
		size = DefaultStreamQueueSize
	}
	return &streamState{
		queue: make(chan *networkv1.GossipResponse, size),
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

	metrics         *relayMetrics
	queueSize       int
	dropAlertRatio  float64
	dropLogCooldown time.Duration
	logger          *slog.Logger

	enqueued atomic.Uint64
	dropped  atomic.Uint64
	lastLog  atomic.Int64
}

// NewRelay constructs a Relay without any attached stream or server.
func NewRelay(opts ...RelayOption) *Relay {
	relay := &Relay{
		metrics:         defaultRelayMetrics(),
		queueSize:       DefaultStreamQueueSize,
		dropAlertRatio:  defaultDropAlertRatio,
		dropLogCooldown: dropLogCooldown,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(relay)
		}
	}
	if relay.queueSize <= 0 {
		relay.queueSize = DefaultStreamQueueSize
	}
	if relay.dropLogCooldown <= 0 {
		relay.dropLogCooldown = dropLogCooldown
	}
	if relay.dropAlertRatio < 0 {
		relay.dropAlertRatio = 0
	}
	return relay
}

// RelayOption customises relay construction.
type RelayOption func(*Relay)

// WithRelayQueueSize overrides the default queue size used for new gossip streams.
func WithRelayQueueSize(size int) RelayOption {
	return func(r *Relay) {
		if r != nil {
			r.queueSize = size
		}
	}
}

// WithRelayLogger attaches a structured logger for relay diagnostics.
func WithRelayLogger(logger *slog.Logger) RelayOption {
	return func(r *Relay) {
		if r != nil {
			r.logger = logger
		}
	}
}

// WithRelayDropAlertRatio sets the drop-rate threshold that triggers structured logging.
// Ratios at or below zero disable the alert.
func WithRelayDropAlertRatio(ratio float64) RelayOption {
	return func(r *Relay) {
		if r != nil {
			r.dropAlertRatio = ratio
		}
	}
}

// WithRelayDropLogCooldown sets the minimum duration between drop alert log entries.
func WithRelayDropLogCooldown(cooldown time.Duration) RelayOption {
	return func(r *Relay) {
		if r != nil {
			r.dropLogCooldown = cooldown
		}
	}
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
	occupancy := len(stream.queue)
	if m := r.metrics; m != nil {
		m.occupancy.Set(float64(occupancy))
	}
	select {
	case stream.queue <- envelope:
		if m := r.metrics; m != nil {
			m.enqueued.Inc()
		}
		r.enqueued.Add(1)
		return true
	default:
		if m := r.metrics; m != nil {
			m.dropped.Inc()
		}
		totalDropped := r.dropped.Add(1)
		r.maybeLogDrop(totalDropped, occupancy)
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
		return errors.New("nil gossip stream")
	}
	state := newStreamState(r.queueSize)
	r.swapStream(state)

	senderCtx, senderCancel := context.WithCancel(stream.Context())
	defer senderCancel()

	sendErr := make(chan error, 1)
	notify := func(err error) {
		select {
		case sendErr <- err:
		default:
		}
	}

	go func() {
		defer notify(nil)
		for {
			select {
			case <-senderCtx.Done():
				return
			case <-state.done:
				return
			case envelope, ok := <-state.queue:
				if !ok {
					return
				}
				if err := stream.Send(envelope); err != nil {
					notify(err)
					senderCancel()
					state.close()
					return
				}
			}
		}
	}()

	defer func() {
		state.close()
		<-sendErr // drain sender goroutine result
	}()

	for {
		incoming, err := stream.Recv()
		if err != nil {
			senderCancel()
			state.close()
			if !errors.Is(err, context.Canceled) {
				notify(err)
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
				if logger := r.logger; logger != nil {
					logger.Error("network relay broadcast failed", slog.Any("error", err))
				}
			}
		case *networkv1.NetworkEnvelope_Heartbeat:
			// Heartbeats flowing from consensus are ignored for now.
		default:
			if logger := r.logger; logger != nil {
				logger.Warn("network relay received unknown envelope", slog.String("type", fmt.Sprintf("%T", event)))
			}
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
	if m := r.metrics; m != nil {
		m.occupancy.Set(0)
	}
}

func (r *Relay) clearStream(state *streamState) {
	r.mu.Lock()
	if r.stream == state {
		r.stream = nil
	}
	r.mu.Unlock()
	state.close()
	if m := r.metrics; m != nil {
		m.occupancy.Set(0)
	}
}

func (r *Relay) maybeLogDrop(dropped uint64, occupancy int) {
	if r == nil || r.logger == nil {
		return
	}
	if r.dropAlertRatio <= 0 {
		return
	}
	enqueued := r.enqueued.Load()
	total := dropped + enqueued
	if total == 0 {
		return
	}
	ratio := float64(dropped) / float64(total)
	if ratio < r.dropAlertRatio {
		return
	}
	now := time.Now()
	last := time.Unix(0, r.lastLog.Load())
	if now.Sub(last) < r.dropLogCooldown {
		return
	}
	r.lastLog.Store(now.UnixNano())
	r.logger.Warn("relay queue saturated; dropping envelopes",
		slog.Float64("drop_ratio", ratio),
		slog.Float64("threshold", r.dropAlertRatio),
		slog.Int("queue_size", r.queueSize),
		slog.Int("queue_occupancy", occupancy),
		slog.Uint64("dropped_total", dropped),
		slog.Uint64("enqueued_total", enqueued),
	)
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
