package p2p

import (
	"context"
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

var (
	metricsInitOnce sync.Once
	sharedMetrics   *networkMetrics
)

type networkMetrics struct {
	peerScore       *prometheus.GaugeVec
	peerLatency     *prometheus.GaugeVec
	peerUseful      *prometheus.GaugeVec
	peerMisbehavior *prometheus.GaugeVec
	handshake       *prometheus.CounterVec
	gossip          *prometheus.CounterVec

	meter            metric.Meter
	handshakeCounter metric.Int64Counter
	gossipCounter    metric.Int64Counter
	latencyHistogram metric.Float64Histogram
}

func newNetworkMetrics() *networkMetrics {
	metricsInitOnce.Do(func() {
		nm := &networkMetrics{
			peerScore: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "nhb_p2p_peer_score",
				Help: "Composite reputation score per peer.",
			}, []string{"peer"}),
			peerLatency: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "nhb_p2p_peer_latency_ms",
				Help: "Latency exponential moving average per peer.",
			}, []string{"peer"}),
			peerUseful: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "nhb_p2p_peer_useful_events",
				Help: "Count of useful messages processed per peer.",
			}, []string{"peer"}),
			peerMisbehavior: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "nhb_p2p_peer_misbehavior",
				Help: "Count of misbehavior incidents per peer.",
			}, []string{"peer"}),
			handshake: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "nhb_p2p_handshakes_total",
				Help: "Total handshake outcomes.",
			}, []string{"result"}),
			gossip: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "nhb_p2p_gossip_messages_total",
				Help: "Count of gossip/control messages by direction and type.",
			}, []string{"direction", "type"}),
		}
		prometheus.MustRegister(nm.peerScore, nm.peerLatency, nm.peerUseful, nm.peerMisbehavior, nm.handshake, nm.gossip)
		nm.initMeter()
		sharedMetrics = nm
	})
	return sharedMetrics
}

func (m *networkMetrics) initMeter() {
	meter := otel.GetMeterProvider().Meter("nhbchain/p2p")
	counter, err := meter.Int64Counter("nhb.p2p.handshakes")
	if err != nil {
		fallback := noop.NewMeterProvider().Meter("nhbchain/p2p")
		counter, _ = fallback.Int64Counter("nhb.p2p.handshakes")
		meter = fallback
	}
	gossipCounter, err := meter.Int64Counter("nhb.p2p.gossip")
	if err != nil {
		fallback := noop.NewMeterProvider().Meter("nhbchain/p2p")
		gossipCounter, _ = fallback.Int64Counter("nhb.p2p.gossip")
		meter = fallback
	}
	latency, err := meter.Float64Histogram("nhb.p2p.latency_ms")
	if err != nil {
		fallback := noop.NewMeterProvider().Meter("nhbchain/p2p")
		latency, _ = fallback.Float64Histogram("nhb.p2p.latency_ms")
		meter = fallback
	}
	m.meter = meter
	m.handshakeCounter = counter
	m.gossipCounter = gossipCounter
	m.latencyHistogram = latency
}

func (m *networkMetrics) observePeerStatus(peerID string, status ReputationStatus) {
	if m == nil || peerID == "" {
		return
	}
	m.peerScore.WithLabelValues(peerID).Set(float64(status.Score))
	m.peerLatency.WithLabelValues(peerID).Set(status.LatencyMS)
	m.peerUseful.WithLabelValues(peerID).Set(float64(status.Useful))
	m.peerMisbehavior.WithLabelValues(peerID).Set(float64(status.Misbehavior))
	if m.latencyHistogram != nil && status.LatencyMS > 0 {
		m.latencyHistogram.Record(
			contextBackground(),
			status.LatencyMS,
			metric.WithAttributes(attribute.String("peer", peerID)),
		)
	}
}

func (m *networkMetrics) recordHandshake(result string) {
	if m == nil {
		return
	}
	if result == "" {
		result = "unknown"
	}
	m.handshake.WithLabelValues(result).Inc()
	if m.handshakeCounter != nil {
		m.handshakeCounter.Add(
			contextBackground(),
			1,
			metric.WithAttributes(attribute.String("result", result)),
		)
	}
}

func (m *networkMetrics) recordGossip(direction string, msgType byte) {
	if m == nil {
		return
	}
	label := fmt.Sprintf("0x%02x", msgType)
	if direction == "" {
		direction = "unknown"
	}
	m.gossip.WithLabelValues(direction, label).Inc()
	if m.gossipCounter != nil {
		m.gossipCounter.Add(
			contextBackground(),
			1,
			metric.WithAttributes(
				attribute.String("direction", direction),
				attribute.String("type", label),
			),
		)
	}
}

func (m *networkMetrics) removePeer(peerID string) {
	if m == nil || peerID == "" {
		return
	}
	m.peerScore.DeleteLabelValues(peerID)
	m.peerLatency.DeleteLabelValues(peerID)
	m.peerUseful.DeleteLabelValues(peerID)
	m.peerMisbehavior.DeleteLabelValues(peerID)
}

var backgroundOnce sync.Once
var backgroundContext context.Context

func contextBackground() context.Context {
	backgroundOnce.Do(func() {
		backgroundContext = context.Background()
	})
	return backgroundContext
}
