package observability

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type moduleMetrics struct {
	requests  *prometheus.CounterVec
	errors    *prometheus.CounterVec
	latency   *prometheus.HistogramVec
	throttles *prometheus.CounterVec
}

var (
	moduleMetricsOnce sync.Once
	moduleRegistry    *moduleMetrics

	swapStableOnce sync.Once
	swapStableReg  *SwapStableMetrics

	payoutMetricsOnce sync.Once
	payoutRegistry    *PayoutdMetrics

	oracleMetricsOnce sync.Once
	oracleRegistry    *OracleAttesterdMetrics

	consensusMetricsOnce sync.Once
	consensusRegistry    *consensusMetrics
)

// ModuleMetrics returns the lazily-initialised module metrics registry used to
// record RPC module activity.
func ModuleMetrics() *moduleMetrics {
	moduleMetricsOnce.Do(func() {
		moduleRegistry = &moduleMetrics{
			requests: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "module",
				Name:      "requests_total",
				Help:      "Total JSON-RPC module requests segmented by module and method.",
			}, []string{"module", "method", "outcome"}),
			errors: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "module",
				Name:      "errors_total",
				Help:      "Total JSON-RPC module errors segmented by module, method, and status code.",
			}, []string{"module", "method", "status"}),
			latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Namespace: "nhb",
				Subsystem: "module",
				Name:      "request_duration_seconds",
				Help:      "Latency distribution for JSON-RPC module handlers.",
				Buckets:   prometheus.DefBuckets,
			}, []string{"module", "method"}),
			throttles: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "module",
				Name:      "throttles_total",
				Help:      "Count of module requests rejected due to throttling policies.",
			}, []string{"module", "reason"}),
		}
		prometheus.MustRegister(
			moduleRegistry.requests,
			moduleRegistry.errors,
			moduleRegistry.latency,
			moduleRegistry.throttles,
		)
	})
	return moduleRegistry
}

// Observe records the outcome of a module request. The status code should be
// the HTTP status that was ultimately written to the response writer.
func (m *moduleMetrics) Observe(module, method string, status int, duration time.Duration) {
	if m == nil {
		return
	}
	if module == "" {
		module = "unknown"
	}
	if method == "" {
		method = "unknown"
	}
	outcome := "success"
	if status >= 400 {
		outcome = "error"
	}
	m.requests.WithLabelValues(module, method, outcome).Inc()
	if status >= 400 {
		m.errors.WithLabelValues(module, method, fmt.Sprintf("%d", status)).Inc()
	}
	m.latency.WithLabelValues(module, method).Observe(duration.Seconds())
}

// RecordThrottle increments the throttle counter for the supplied module and
// reason. Reasons should be stable strings such as "rate_limit" or
// "quota_exceeded" so dashboards and alerts remain consistent.
func (m *moduleMetrics) RecordThrottle(module, reason string) {
	if m == nil {
		return
	}
	if module == "" {
		module = "unknown"
	}
	if reason == "" {
		reason = "unspecified"
	}
	m.throttles.WithLabelValues(module, reason).Inc()
}

// SwapStableMetrics captures metrics for the experimental stable swap flows.
type SwapStableMetrics struct {
	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
	errors   *prometheus.CounterVec
}

// SwapStable returns the singleton metrics registry for swapd stable endpoints.
func SwapStable() *SwapStableMetrics {
	swapStableOnce.Do(func() {
		swapStableReg = &SwapStableMetrics{
			requests: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "swapd_stable",
				Name:      "requests_total",
				Help:      "Count of stable swap operations segmented by step and outcome.",
			}, []string{"operation", "outcome"}),
			latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Namespace: "nhb",
				Subsystem: "swapd_stable",
				Name:      "request_duration_seconds",
				Help:      "Latency distribution for stable swap operations.",
				Buckets:   prometheus.DefBuckets,
			}, []string{"operation"}),
			errors: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "swapd_stable",
				Name:      "errors_total",
				Help:      "Count of stable swap failures segmented by operation and reason.",
			}, []string{"operation", "reason"}),
		}
		prometheus.MustRegister(
			swapStableReg.requests,
			swapStableReg.latency,
			swapStableReg.errors,
		)
	})
	return swapStableReg
}

// Observe records the execution metrics for a stable swap operation.
func (m *SwapStableMetrics) Observe(operation string, duration time.Duration, err error) {
	if m == nil {
		return
	}
	op := strings.TrimSpace(operation)
	if op == "" {
		op = "unknown"
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
		reason := strings.TrimSpace(err.Error())
		if reason == "" {
			reason = "unknown"
		}
		m.errors.WithLabelValues(op, reason).Inc()
	}
	m.requests.WithLabelValues(op, outcome).Inc()
	m.latency.WithLabelValues(op).Observe(duration.Seconds())
}

// PayoutdMetrics wraps collectors tracking payout engine health.
type PayoutdMetrics struct {
	payoutLatency  *prometheus.HistogramVec
	capRemaining   *prometheus.GaugeVec
	capUtilization *prometheus.GaugeVec
	errors         *prometheus.CounterVec
	pauseEngaged   prometheus.Gauge
}

// Payoutd exposes the metrics registry for payoutd.
func Payoutd() *PayoutdMetrics {
	payoutMetricsOnce.Do(func() {
		payoutRegistry = &PayoutdMetrics{
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
			capUtilization: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "payoutd",
				Name:      "cap_utilization",
				Help:      "Ratio of consumed cap for the current payout window (0-1).",
			}, []string{"asset"}),
			errors: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "payoutd",
				Name:      "errors_total",
				Help:      "Count of payout failures segmented by asset and reason.",
			}, []string{"asset", "reason"}),
			pauseEngaged: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "payoutd",
				Name:      "pause_engaged",
				Help:      "Indicates whether the payout processor pause guard is active (1) or not (0).",
			}),
		}
		prometheus.MustRegister(
			payoutRegistry.payoutLatency,
			payoutRegistry.capRemaining,
			payoutRegistry.capUtilization,
			payoutRegistry.errors,
			payoutRegistry.pauseEngaged,
		)
	})
	return payoutRegistry
}

// ObserveLatency records the processing latency for a payout.
func (m *PayoutdMetrics) ObserveLatency(asset string, d time.Duration) {
	if m == nil {
		return
	}
	m.payoutLatency.WithLabelValues(labelAsset(asset)).Observe(d.Seconds())
}

// RecordCap updates the remaining cap and utilisation gauge for an asset.
func (m *PayoutdMetrics) RecordCap(asset string, remaining, total *big.Int) {
	if m == nil {
		return
	}
	label := labelAsset(asset)
	remainingVal := bigToFloat(remaining)
	m.capRemaining.WithLabelValues(label).Set(remainingVal)
	totalVal := bigToFloat(total)
	utilisation := 0.0
	if totalVal > 0 {
		used := totalVal - remainingVal
		if used < 0 {
			used = 0
		}
		utilisation = used / totalVal
		if utilisation > 1 {
			utilisation = 1
		}
	}
	m.capUtilization.WithLabelValues(label).Set(utilisation)
}

// RecordError increments the error counter for the supplied reason.
func (m *PayoutdMetrics) RecordError(asset, reason string) {
	if m == nil {
		return
	}
	if reason = strings.TrimSpace(reason); reason == "" {
		reason = "unspecified"
	}
	m.errors.WithLabelValues(labelAsset(asset), reason).Inc()
}

// SetPause toggles the pause_engaged gauge.
func (m *PayoutdMetrics) SetPause(engaged bool) {
	if m == nil {
		return
	}
	if engaged {
		m.pauseEngaged.Set(1)
		return
	}
	m.pauseEngaged.Set(0)
}

// OracleAttesterdMetrics bundles collectors for voucher minting and freshness tracking.
type OracleAttesterdMetrics struct {
	voucherRate *prometheus.CounterVec
	freshness   *prometheus.GaugeVec
}

type consensusMetrics struct {
	blockInterval prometheus.Gauge
}

// Consensus exposes the metrics registry for consensus level instrumentation.
func Consensus() *consensusMetrics {
	consensusMetricsOnce.Do(func() {
		consensusRegistry = &consensusMetrics{
			blockInterval: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "consensus",
				Name:      "block_interval_seconds",
				Help:      "Interval in seconds between the timestamps of consecutive committed blocks.",
			}),
		}
		prometheus.MustRegister(consensusRegistry.blockInterval)
	})
	return consensusRegistry
}

// RecordBlockInterval updates the block interval gauge with the supplied duration.
func (m *consensusMetrics) RecordBlockInterval(interval time.Duration) {
	if m == nil {
		return
	}
	seconds := interval.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	m.blockInterval.Set(seconds)
}

// OracleAttesterd returns the metrics registry for oracle-attesterd.
func OracleAttesterd() *OracleAttesterdMetrics {
	oracleMetricsOnce.Do(func() {
		oracleRegistry = &OracleAttesterdMetrics{
			voucherRate: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "oracle_attesterd",
				Name:      "voucher_rate",
				Help:      "Count of vouchers minted via oracle attestations.",
			}, []string{"asset"}),
			freshness: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "oracle_attesterd",
				Name:      "oracle_freshness_seconds",
				Help:      "Age in seconds between settlement confirmation and attestation mint time.",
			}, []string{"asset"}),
		}
		prometheus.MustRegister(oracleRegistry.voucherRate, oracleRegistry.freshness)
	})
	return oracleRegistry
}

// RecordVoucherMint increments the voucher mint counter for an asset.
func (m *OracleAttesterdMetrics) RecordVoucherMint(asset string) {
	if m == nil {
		return
	}
	m.voucherRate.WithLabelValues(labelAsset(asset)).Inc()
}

// RecordFreshness records how stale the processed event was.
func (m *OracleAttesterdMetrics) RecordFreshness(asset string, age time.Duration) {
	if m == nil {
		return
	}
	m.freshness.WithLabelValues(labelAsset(asset)).Set(age.Seconds())
}

func labelAsset(asset string) string {
	trimmed := strings.TrimSpace(asset)
	if trimmed == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(trimmed)
}

func bigToFloat(value *big.Int) float64 {
	if value == nil {
		return 0
	}
	floatVal, acc := new(big.Float).SetInt(value).Float64()
	if acc != big.Exact {
		// Guard against NaN/Inf when conversion fails.
		if math.IsNaN(floatVal) || math.IsInf(floatVal, 0) {
			return 0
		}
	}
	return floatVal
}
