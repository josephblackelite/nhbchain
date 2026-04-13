package payoutd

import "nhbchain/observability"

// Metrics exposes Prometheus collectors for payoutd instrumentation.
type Metrics = observability.PayoutdMetrics

// NewMetrics returns a lazily initialised metrics registry.
func NewMetrics() *Metrics { return observability.Payoutd() }
