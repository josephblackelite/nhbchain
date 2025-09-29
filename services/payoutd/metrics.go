package payoutd

import (
	"math"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	metricsOnce   sync.Once
	sharedMetrics *Metrics
)

// Metrics exposes Prometheus collectors for payoutd instrumentation.
type Metrics struct {
	payoutLatency *prometheus.HistogramVec
	capRemaining  *prometheus.GaugeVec
	errors        *prometheus.CounterVec
}

// NewMetrics returns a lazily initialised metrics registry.
func NewMetrics() *Metrics {
	metricsOnce.Do(func() {
		sharedMetrics = &Metrics{
			payoutLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Namespace: "nhb",
				Subsystem: "payoutd",
				Name:      "payout_latency_seconds",
				Help:      "Latency distribution for completed payouts.",
				Buckets:   prometheus.DefBuckets,
			}, []string{"asset"}),
			capRemaining: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "payoutd",
				Name:      "cap_remaining",
				Help:      "Remaining soft cap per asset in integer stable units.",
			}, []string{"asset"}),
			errors: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "payoutd",
				Name:      "errors_total",
				Help:      "Count of payout failures segmented by asset and reason.",
			}, []string{"asset", "reason"}),
		}
		prometheus.MustRegister(sharedMetrics.payoutLatency, sharedMetrics.capRemaining, sharedMetrics.errors)
	})
	return sharedMetrics
}

// ObserveLatency records the processing latency for a payout.
func (m *Metrics) ObserveLatency(asset string, d time.Duration) {
	if m == nil {
		return
	}
	m.payoutLatency.WithLabelValues(labelAsset(asset)).Observe(d.Seconds())
}

// RecordCap updates the remaining cap gauge for an asset.
func (m *Metrics) RecordCap(asset string, remaining *big.Int) {
	if m == nil {
		return
	}
	value := 0.0
	if remaining != nil {
		if floatVal, _ := new(big.Float).SetInt(remaining).Float64(); !math.IsInf(floatVal, 0) && !math.IsNaN(floatVal) {
			value = floatVal
		} else if remaining.Sign() > 0 {
			value = math.MaxFloat64
		}
	}
	m.capRemaining.WithLabelValues(labelAsset(asset)).Set(value)
}

// RecordError increments the error counter for the supplied reason.
func (m *Metrics) RecordError(asset, reason string) {
	if m == nil {
		return
	}
	if reason == "" {
		reason = "unspecified"
	}
	m.errors.WithLabelValues(labelAsset(asset), reason).Inc()
}

func labelAsset(asset string) string {
	if asset == "" {
		return "unknown"
	}
	return strings.ToUpper(asset)
}
