package network

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

type relayMetrics struct {
	enqueued  prometheus.Counter
	dropped   prometheus.Counter
	occupancy prometheus.Gauge
}

var (
	relayMetricsOnce sync.Once
	relayRegistry    *relayMetrics
)

func defaultRelayMetrics() *relayMetrics {
	relayMetricsOnce.Do(func() {
		relayRegistry = &relayMetrics{
			enqueued: prometheus.NewCounter(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "network_relay",
				Name:      "queue_enqueued_total",
				Help:      "Total envelopes successfully enqueued onto the consensus relay stream.",
			}),
			dropped: prometheus.NewCounter(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "network_relay",
				Name:      "queue_dropped_total",
				Help:      "Total envelopes dropped because the consensus relay queue was full.",
			}),
			occupancy: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "network_relay",
				Name:      "queue_occupancy",
				Help:      "Point-in-time occupancy of the consensus relay send queue prior to enqueue attempts.",
			}),
		}
		prometheus.MustRegister(
			relayRegistry.enqueued,
			relayRegistry.dropped,
			relayRegistry.occupancy,
		)
	})
	return relayRegistry
}
