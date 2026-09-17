package metrics

import (
	"fmt"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

type PotsoMetrics struct {
	evidenceAccepted     *prometheus.CounterVec
	penaltyApplied       *prometheus.CounterVec
	epochPool            prometheus.Gauge
	rewardsSum           *prometheus.GaugeVec
	webhookFailures      *prometheus.CounterVec
	roundingDust         *prometheus.GaugeVec
	heartbeatCount       *prometheus.CounterVec
	heartbeatRateLimited *prometheus.CounterVec
	heartbeatUniquePeers *prometheus.GaugeVec
	heartbeatAvgSession  *prometheus.GaugeVec
	heartbeatWash        *prometheus.CounterVec
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
			heartbeatCount: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "potso_heartbeat_total",
				Help: "Count of accepted heartbeats per address and epoch.",
			}, []string{"epoch", "address"}),
			heartbeatRateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "potso_heartbeat_rate_limited_total",
				Help: "Count of heartbeats rejected by the per-address rate limit.",
			}, []string{"epoch", "address"}),
			heartbeatUniquePeers: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "potso_heartbeat_unique_peers",
				Help: "Unique addresses submitting heartbeats within the epoch.",
			}, []string{"epoch"}),
			heartbeatAvgSession: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "potso_heartbeat_avg_session_seconds",
				Help: "Average session length derived from uptime deltas per epoch.",
			}, []string{"epoch"}),
			heartbeatWash: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "potso_heartbeat_wash_total",
				Help: "Heartbeats recorded while emissions were disabled (wash engagement signal).",
			}, []string{"epoch", "address"}),
		}
		prometheus.MustRegister(
			potsoRegistry.evidenceAccepted,
			potsoRegistry.penaltyApplied,
			potsoRegistry.epochPool,
			potsoRegistry.rewardsSum,
			potsoRegistry.webhookFailures,
			potsoRegistry.roundingDust,
			potsoRegistry.heartbeatCount,
			potsoRegistry.heartbeatRateLimited,
			potsoRegistry.heartbeatUniquePeers,
			potsoRegistry.heartbeatAvgSession,
			potsoRegistry.heartbeatWash,
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

func (m *PotsoMetrics) IncHeartbeat(address string, epoch uint64) {
	if m == nil {
		return
	}
	epochLabel := fmt.Sprintf("%d", epoch)
	m.heartbeatCount.WithLabelValues(epochLabel, normaliseAddress(address)).Inc()
}

func (m *PotsoMetrics) IncHeartbeatRateLimited(address string, epoch uint64) {
	if m == nil {
		return
	}
	epochLabel := fmt.Sprintf("%d", epoch)
	m.heartbeatRateLimited.WithLabelValues(epochLabel, normaliseAddress(address)).Inc()
}

func (m *PotsoMetrics) SetHeartbeatUniquePeers(epoch uint64, count float64) {
	if m == nil {
		return
	}
	epochLabel := fmt.Sprintf("%d", epoch)
	m.heartbeatUniquePeers.WithLabelValues(epochLabel).Set(count)
}

func (m *PotsoMetrics) SetHeartbeatAvgSession(epoch uint64, seconds float64) {
	if m == nil {
		return
	}
	epochLabel := fmt.Sprintf("%d", epoch)
	m.heartbeatAvgSession.WithLabelValues(epochLabel).Set(seconds)
}

func (m *PotsoMetrics) IncHeartbeatWash(address string, epoch uint64) {
	if m == nil {
		return
	}
	epochLabel := fmt.Sprintf("%d", epoch)
	m.heartbeatWash.WithLabelValues(epochLabel, normaliseAddress(address)).Inc()
}

func (m *PotsoMetrics) InitHeartbeatEpoch(epoch uint64) {
	if m == nil {
		return
	}
	epochLabel := fmt.Sprintf("%d", epoch)
	m.heartbeatUniquePeers.WithLabelValues(epochLabel).Set(0)
	m.heartbeatAvgSession.WithLabelValues(epochLabel).Set(0)
}

func (m *PotsoMetrics) ResetHeartbeatMetrics() {
	if m == nil {
		return
	}
	m.heartbeatCount.Reset()
	m.heartbeatRateLimited.Reset()
	m.heartbeatUniquePeers.Reset()
	m.heartbeatAvgSession.Reset()
	m.heartbeatWash.Reset()
}

func (m *PotsoMetrics) HeartbeatCounterVec() *prometheus.CounterVec {
	if m == nil {
		return nil
	}
	return m.heartbeatCount
}

func (m *PotsoMetrics) HeartbeatRateLimitedVec() *prometheus.CounterVec {
	if m == nil {
		return nil
	}
	return m.heartbeatRateLimited
}

func (m *PotsoMetrics) HeartbeatUniquePeersGauge() *prometheus.GaugeVec {
	if m == nil {
		return nil
	}
	return m.heartbeatUniquePeers
}

func (m *PotsoMetrics) HeartbeatAvgSessionGauge() *prometheus.GaugeVec {
	if m == nil {
		return nil
	}
	return m.heartbeatAvgSession
}

func (m *PotsoMetrics) HeartbeatWashVec() *prometheus.CounterVec {
	if m == nil {
		return nil
	}
	return m.heartbeatWash
}

func normaliseAddress(address string) string {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return "unknown"
	}
	return strings.ToLower(trimmed)
}
