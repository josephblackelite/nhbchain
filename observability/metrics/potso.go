package metrics

import (
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

type PotsoMetrics struct {
	evidenceAccepted *prometheus.CounterVec
	penaltyApplied   *prometheus.CounterVec
	epochPool        prometheus.Gauge
	rewardsSum       *prometheus.GaugeVec
	webhookFailures  *prometheus.CounterVec
	roundingDust     *prometheus.GaugeVec
}

var (
	potsoOnce     sync.Once
	potsoRegistry *PotsoMetrics
)

func Potso() *PotsoMetrics {
	potsoOnce.Do(func() {
		potsoRegistry = &PotsoMetrics{
			evidenceAccepted: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "potso_evidence_accepted_total",
				Help: "Count of accepted evidence submissions by type.",
			}, []string{"type"}),
			penaltyApplied: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "potso_penalty_applied_total",
				Help: "Count of penalty executions applied by type.",
			}, []string{"type"}),
			epochPool: prometheus.NewGauge(prometheus.GaugeOpts{
				Name: "potso_epoch_pool",
				Help: "Current token pool available for the active epoch.",
			}),
			rewardsSum: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "potso_rewards_sum",
				Help: "Total reward emissions per epoch.",
			}, []string{"epoch"}),
			webhookFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "potso_webhook_failures_total",
				Help: "Number of failed webhook delivery attempts by destination.",
			}, []string{"destination"}),
			roundingDust: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "potso_rounding_dust",
				Help: "Cumulative rounding remainder recorded per epoch.",
			}, []string{"epoch"}),
		}
		prometheus.MustRegister(
			potsoRegistry.evidenceAccepted,
			potsoRegistry.penaltyApplied,
			potsoRegistry.epochPool,
			potsoRegistry.rewardsSum,
			potsoRegistry.webhookFailures,
			potsoRegistry.roundingDust,
		)
	})
	return potsoRegistry
}

func (m *PotsoMetrics) ObserveEvidenceAccepted(kind string) {
	if m == nil {
		return
	}
	if kind == "" {
		kind = "unknown"
	}
	m.evidenceAccepted.WithLabelValues(kind).Inc()
}

func (m *PotsoMetrics) ObservePenaltyApplied(kind string) {
	if m == nil {
		return
	}
	if kind == "" {
		kind = "unknown"
	}
	m.penaltyApplied.WithLabelValues(kind).Inc()
}

func (m *PotsoMetrics) SetEpochPool(amount float64) {
	if m == nil {
		return
	}
	m.epochPool.Set(amount)
}

func (m *PotsoMetrics) ObserveRewardsSum(epoch uint64, amount float64) {
	if m == nil {
		return
	}
	label := fmt.Sprintf("%d", epoch)
	m.rewardsSum.WithLabelValues(label).Set(amount)
}

func (m *PotsoMetrics) ObserveRoundingDust(epoch uint64, dust float64) {
	if m == nil {
		return
	}
	label := fmt.Sprintf("%d", epoch)
	m.roundingDust.WithLabelValues(label).Set(dust)
}

func (m *PotsoMetrics) IncWebhookFailure(destination string) {
	if m == nil {
		return
	}
	if destination == "" {
		destination = "unknown"
	}
	m.webhookFailures.WithLabelValues(destination).Inc()
}

func (m *PotsoMetrics) InitRewardEpoch(epoch uint64) {
	if m == nil {
		return
	}
	label := fmt.Sprintf("%d", epoch)
	m.rewardsSum.WithLabelValues(label).Add(0)
	m.roundingDust.WithLabelValues(label).Set(0)
}

func (m *PotsoMetrics) InitEvidenceType(kind string) {
	if m == nil {
		return
	}
	if kind == "" {
		kind = "unknown"
	}
	m.evidenceAccepted.WithLabelValues(kind).Add(0)
	m.penaltyApplied.WithLabelValues(kind).Add(0)
}

func (m *PotsoMetrics) InitWebhookDestination(destination string) {
	if m == nil {
		return
	}
	if destination == "" {
		destination = "unknown"
	}
	m.webhookFailures.WithLabelValues(destination).Add(0)
}
