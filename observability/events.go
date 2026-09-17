package observability

import (
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

type eventMetrics struct {
	transfers *prometheus.CounterVec
}

var (
	eventMetricsOnce sync.Once
	eventRegistry    *eventMetrics
)

// Events returns the metrics registry tracking structured chain events.
func Events() *eventMetrics {
	eventMetricsOnce.Do(func() {
		eventRegistry = &eventMetrics{
			transfers: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "events",
				Name:      "transfers_total",
				Help:      "Count of native transfers segmented by asset.",
			}, []string{"asset"}),
		}
		prometheus.MustRegister(eventRegistry.transfers)
	})
	return eventRegistry
}

// RecordTransfer increments the transfer counter for the supplied asset ticker.
func (m *eventMetrics) RecordTransfer(asset string) {
	if m == nil {
		return
	}
	normalized := strings.TrimSpace(strings.ToUpper(asset))
	if normalized == "" {
		normalized = "UNKNOWN"
	}
	m.transfers.WithLabelValues(normalized).Inc()
}
