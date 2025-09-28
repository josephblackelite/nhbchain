package observability

import (
	"fmt"
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
