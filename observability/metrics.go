package observability

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"
	"time"

	"nhbchain/mempool"

	"github.com/prometheus/client_golang/prometheus"
)

type moduleMetrics struct {
	requests  *prometheus.CounterVec
	errors    *prometheus.CounterVec
	latency   *prometheus.HistogramVec
	throttles *prometheus.CounterVec
}

// RPCMetrics captures instrumentation for RPC-specific safeguards.
type RPCMetrics struct {
	limiterHits *prometheus.CounterVec
}

// MempoolMetrics surfaces instrumentation for POS QoS accounting.
type MempoolMetrics struct {
	posLaneFill    prometheus.Gauge
	posLaneBacklog *prometheus.GaugeVec
	posEnqueued    prometheus.Counter
	posFinality    prometheus.Histogram
}

// PaymasterMetrics captures observability counters for automatic paymaster top-ups.
type PaymasterMetrics struct {
	topups *prometheus.CounterVec
	minted *prometheus.CounterVec
}

// StakingMetrics captures staking module health and activity counters.
type StakingMetrics struct {
	rewardsPaid          prometheus.Counter
	paused               prometheus.Gauge
	totalStaked          *prometheus.GaugeVec
	capHit               prometheus.Counter
	indexPersistFailures prometheus.Counter
}

// POSLifecycleMetrics captures automatic expiry sweep counters for POS authorizations.
type POSLifecycleMetrics struct {
	authExpired prometheus.Counter
}

// SecurityMetrics captures security-sensitive configuration signals.
type SecurityMetrics struct {
	insecureBinds *prometheus.CounterVec
}

// SupplyMetrics captures token supply telemetry.
type SupplyMetrics struct {
	total *prometheus.GaugeVec
}

// Loyalty returns the singleton registry tracking loyalty reward budget usage.
func Loyalty() *LoyaltyMetrics {
	loyaltyMetricsOnce.Do(func() {
		loyaltyRegistry = &LoyaltyMetrics{
			budget: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "loyalty",
				Name:      "budget_zn",
				Help:      "Remaining daily loyalty budget expressed in ZNHB.",
			}),
			demand: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "loyalty",
				Name:      "demand_zn",
				Help:      "Total base rewards demanded during the block expressed in ZNHB.",
			}),
			ratio: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "loyalty",
				Name:      "prorate_ratio",
				Help:      "Applied pro-rate ratio for base rewards (0-1).",
			}),
			paidToday: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "loyalty",
				Name:      "paid_today_zn",
				Help:      "Total base rewards paid out today expressed in ZNHB.",
			}),
			fallbacks: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "loyalty",
				Name:      "price_fallback_total",
				Help:      "Count of loyalty price guard fallbacks segmented by strategy.",
			}, []string{"strategy"}),
		}
		prometheus.MustRegister(
			loyaltyRegistry.budget,
			loyaltyRegistry.demand,
			loyaltyRegistry.ratio,
			loyaltyRegistry.paidToday,
			loyaltyRegistry.fallbacks,
		)
	})
	return loyaltyRegistry
}

// RecordBudget updates the loyalty budget gauges.
func (m *LoyaltyMetrics) RecordBudget(budget, demand, ratio, paid float64) {
	if m == nil {
		return
	}
	m.budget.Set(budget)
	m.demand.Set(demand)
	m.ratio.Set(ratio)
	m.paidToday.Set(paid)
}

// RatioGauge exposes the loyalty pro-rate ratio gauge for testing.
func (m *LoyaltyMetrics) RatioGauge() prometheus.Gauge {
	if m == nil {
		return nil
	}
	return m.ratio
}

// LoyaltyMetrics captures the daily loyalty budget utilisation telemetry.
type LoyaltyMetrics struct {
	budget    prometheus.Gauge
	demand    prometheus.Gauge
	ratio     prometheus.Gauge
	paidToday prometheus.Gauge
	fallbacks *prometheus.CounterVec
}

// RecordGuardFallback increments the fallback counter for the supplied strategy label.
func (m *LoyaltyMetrics) RecordGuardFallback(strategy string) {
	if m == nil || m.fallbacks == nil {
		return
	}
	trimmed := strings.TrimSpace(strategy)
	if trimmed == "" {
		trimmed = "unknown"
	}
	m.fallbacks.WithLabelValues(trimmed).Inc()
}

// FallbackCounterVec exposes the fallback counter vector for testing.
func (m *LoyaltyMetrics) FallbackCounterVec() *prometheus.CounterVec {
	if m == nil {
		return nil
	}
	return m.fallbacks
}

var (
	moduleMetricsOnce sync.Once
	moduleRegistry    *moduleMetrics

	rpcMetricsOnce sync.Once
	rpcRegistry    *RPCMetrics

	swapStableOnce sync.Once
	swapStableReg  *SwapStableMetrics

	payoutMetricsOnce sync.Once
	payoutRegistry    *PayoutdMetrics

	oracleMetricsOnce sync.Once
	oracleRegistry    *OracleAttesterdMetrics

	consensusMetricsOnce sync.Once
	consensusRegistry    *consensusMetrics

	mempoolMetricsOnce sync.Once
	mempoolRegistry    *MempoolMetrics

	paymasterMetricsOnce sync.Once
	paymasterRegistry    *PaymasterMetrics

	stakingMetricsOnce sync.Once
	stakingRegistry    *StakingMetrics

	loyaltyMetricsOnce sync.Once
	loyaltyRegistry    *LoyaltyMetrics

	posLifecycleOnce     sync.Once
	posLifecycleRegistry *POSLifecycleMetrics

	securityMetricsOnce sync.Once
	securityRegistry    *SecurityMetrics

	supplyMetricsOnce sync.Once
	supplyRegistry    *SupplyMetrics
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

// RPC returns the singleton registry for RPC-specific safeguards such as rate
// limiting.
func RPC() *RPCMetrics {
	rpcMetricsOnce.Do(func() {
		rpcRegistry = &RPCMetrics{
			limiterHits: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "rpc",
				Name:      "limiter_hits_total",
				Help:      "Count of RPC rate limiter rejections segmented by attribution scope.",
			}, []string{"scope"}),
		}
		prometheus.MustRegister(rpcRegistry.limiterHits)
	})
	return rpcRegistry
}

// Security returns the metrics registry tracking sensitive configuration states.
func Security() *SecurityMetrics {
	securityMetricsOnce.Do(func() {
		securityRegistry = &SecurityMetrics{
			insecureBinds: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "security",
				Name:      "insecure_binds_total",
				Help:      "Count of listeners started with AllowInsecure segmented by loopback safety.",
			}, []string{"service", "loopback"}),
		}
		prometheus.MustRegister(securityRegistry.insecureBinds)
	})
	return securityRegistry
}

// Supply exposes the metrics registry tracking token supply totals.
func Supply() *SupplyMetrics {
	supplyMetricsOnce.Do(func() {
		supplyRegistry = &SupplyMetrics{
			total: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "token",
				Name:      "supply_total",
				Help:      "Total circulating supply per token in wei.",
			}, []string{"token"}),
		}
		prometheus.MustRegister(supplyRegistry.total)
	})
	return supplyRegistry
}

// RecordTotal updates the tracked supply gauge for the token symbol.
func (m *SupplyMetrics) RecordTotal(symbol string, total *big.Int) {
	if m == nil {
		return
	}
	value := bigToFloat(total)
	if value < 0 {
		value = 0
	}
	m.total.WithLabelValues(labelAsset(symbol)).Set(value)
}

// RecordInsecureBind records whether an insecure listener bound to a loopback interface.
func (m *SecurityMetrics) RecordInsecureBind(service string, loopback bool) {
	if m == nil {
		return
	}
	normalized := strings.TrimSpace(service)
	if normalized == "" {
		normalized = "unknown"
	}
	loopbackLabel := "false"
	if loopback {
		loopbackLabel = "true"
	}
	m.insecureBinds.WithLabelValues(normalized, loopbackLabel).Inc()
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

// RecordLimiterHit increments the limiter hit counter for the supplied
// attribution scope.
func (m *RPCMetrics) RecordLimiterHit(scope string) {
	if m == nil {
		return
	}
	normalized := strings.TrimSpace(scope)
	if normalized == "" {
		normalized = "unknown"
	}
	m.limiterHits.WithLabelValues(normalized).Inc()
}

// LimiterHits exposes the limiter hit counter for testing.
func (m *RPCMetrics) LimiterHits() *prometheus.CounterVec {
	if m == nil {
		return nil
	}
	return m.limiterHits
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

// Mempool returns the singleton registry tracking POS QoS health.
func Mempool() *MempoolMetrics {
	mempoolMetricsOnce.Do(func() {
		mempoolRegistry = &MempoolMetrics{
			posLaneFill: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "mempool",
				Name:      "pos_lane_fill",
				Help:      "Fill ratio for the POS-reserved transaction lane.",
			}),
			posLaneBacklog: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "mempool",
				Name:      "pos_lane_backlog",
				Help:      "Count of POS-tagged transfers segmented by asset.",
			}, []string{"asset"}),
			posEnqueued: prometheus.NewCounter(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "mempool",
				Name:      "pos_tx_enqueued_total",
				Help:      "Count of POS-tagged transactions admitted to the mempool.",
			}),
			posFinality: prometheus.NewHistogram(prometheus.HistogramOpts{
				Namespace: "nhb",
				Subsystem: "mempool",
				Name:      "pos_p95_finality_ms",
				Help:      "Latency for POS-tagged transactions from enqueue to finality in milliseconds.",
				Buckets:   []float64{50, 100, 200, 400, 800, 1_600, 3_200, 6_400, 12_800},
			}),
		}
		prometheus.MustRegister(
			mempoolRegistry.posLaneFill,
			mempoolRegistry.posLaneBacklog,
			mempoolRegistry.posEnqueued,
			mempoolRegistry.posFinality,
		)
	})
	return mempoolRegistry
}

// Paymaster returns the singleton metrics registry tracking automatic paymaster top-ups.
func Paymaster() *PaymasterMetrics {
	paymasterMetricsOnce.Do(func() {
		paymasterRegistry = &PaymasterMetrics{
			topups: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "paymaster",
				Name:      "autotopups_total",
				Help:      "Count of automatic paymaster top-ups segmented by outcome.",
			}, []string{"outcome"}),
			minted: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "paymaster",
				Name:      "autotopup_amount_wei_total",
				Help:      "Total wei minted through automatic paymaster top-ups segmented by outcome.",
			}, []string{"outcome"}),
		}
		prometheus.MustRegister(
			paymasterRegistry.topups,
			paymasterRegistry.minted,
		)
	})
	return paymasterRegistry
}

// POSLifecycle returns the singleton metrics registry tracking authorization expiry sweeps.
func POSLifecycle() *POSLifecycleMetrics {
	posLifecycleOnce.Do(func() {
		posLifecycleRegistry = &POSLifecycleMetrics{
			authExpired: prometheus.NewCounter(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "pos",
				Name:      "auth_expired_total",
				Help:      "Count of POS authorizations automatically voided after expiry.",
			}),
		}
		prometheus.MustRegister(posLifecycleRegistry.authExpired)
	})
	return posLifecycleRegistry
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

// RecordPOSLaneFill updates the gauge describing how saturated the POS lane is
// relative to its reserved capacity. When the reservation is disabled, the
// gauge reports the absolute POS backlog size.
func (m *MempoolMetrics) RecordPOSLaneFill(usage mempool.Usage) {
	if m == nil {
		return
	}
	if usage.Target <= 0 {
		if usage.TotalPOS == 0 {
			m.posLaneFill.Set(0)
		} else {
			m.posLaneFill.Set(float64(usage.TotalPOS))
		}
		m.recordPOSBacklog(usage.POSByAsset)
		return
	}
	ratio := float64(usage.TotalPOS) / float64(usage.Target)
	m.posLaneFill.Set(ratio)
	m.recordPOSBacklog(usage.POSByAsset)
}

func (m *MempoolMetrics) recordPOSBacklog(byAsset map[string]int) {
	if m == nil || m.posLaneBacklog == nil {
		return
	}
	counts := byAsset
	if counts == nil {
		counts = map[string]int{}
	}
	// Always emit gauges for NHB and ZNHB so dashboards can chart both
	// assets even when empty.
	m.posLaneBacklog.WithLabelValues("nhb").Set(float64(counts["nhb"]))
	m.posLaneBacklog.WithLabelValues("znhb").Set(float64(counts["znhb"]))
	for asset, count := range counts {
		if asset == "nhb" || asset == "znhb" {
			continue
		}
		m.posLaneBacklog.WithLabelValues(asset).Set(float64(count))
	}
}

// RecordPOSEnqueued increments the counter tracking how many POS transactions
// have been accepted.
func (m *MempoolMetrics) RecordPOSEnqueued() {
	if m == nil {
		return
	}
	m.posEnqueued.Inc()
}

// ObservePOSFinality records the enqueue-to-commit latency in milliseconds for
// a POS-tagged transaction.
func (m *MempoolMetrics) ObservePOSFinality(latency time.Duration) {
	if m == nil {
		return
	}
	if latency <= 0 {
		m.posFinality.Observe(0)
		return
	}
	m.posFinality.Observe(float64(latency.Milliseconds()))
}

// RecordAuthExpired increments the automatic expiry counter.
func (m *POSLifecycleMetrics) RecordAuthExpired() {
	if m == nil {
		return
	}
	m.authExpired.Inc()
}

// AuthExpiredCounter exposes the underlying counter for testing.
func (m *POSLifecycleMetrics) AuthExpiredCounter() prometheus.Counter {
	if m == nil {
		return nil
	}
	return m.authExpired
}

// RecordAutoTopUp tracks the outcome of an automatic paymaster top-up and the minted amount in wei.
func (m *PaymasterMetrics) RecordAutoTopUp(outcome string, amount *big.Int) {
	if m == nil {
		return
	}
	label := strings.TrimSpace(outcome)
	if label == "" {
		label = "unknown"
	}
	m.topups.WithLabelValues(label).Inc()
	if amount != nil && amount.Sign() > 0 {
		mintedFloat, _ := new(big.Float).SetInt(amount).Float64()
		if mintedFloat > 0 {
			m.minted.WithLabelValues(label).Add(mintedFloat)
		}
	}
}

// Staking exposes the metrics registry tracking staking activity.
func Staking() *StakingMetrics {
	stakingMetricsOnce.Do(func() {
		stakingRegistry = &StakingMetrics{
			rewardsPaid: prometheus.NewCounter(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "staking",
				Name:      "rewards_paid_zn",
				Help:      "Total ZapNHB rewards paid out by staking claims.",
			}),
			paused: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "staking",
				Name:      "paused",
				Help:      "Whether staking mutations are currently paused (1) or active (0).",
			}),
			totalStaked: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: "nhb",
				Subsystem: "staking",
				Name:      "total_staked",
				Help:      "Current ZapNHB stake locked per account in wei.",
			}, []string{"account"}),
			capHit: prometheus.NewCounter(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "staking",
				Name:      "cap_hit",
				Help:      "Count of staking claims that exhausted the configured emission cap.",
			}),
			indexPersistFailures: prometheus.NewCounter(prometheus.CounterOpts{
				Namespace: "nhb",
				Subsystem: "staking",
				Name:      "index_persist_failures_total",
				Help:      "Count of staking reward index persistence failures.",
			}),
		}
		prometheus.MustRegister(
			stakingRegistry.rewardsPaid,
			stakingRegistry.paused,
			stakingRegistry.totalStaked,
			stakingRegistry.capHit,
			stakingRegistry.indexPersistFailures,
		)
	})
	return stakingRegistry
}

// RecordRewardsPaid tracks the cumulative staking rewards distributed.
func (m *StakingMetrics) RecordRewardsPaid(amount *big.Int) {
	if m == nil {
		return
	}
	if amount == nil || amount.Sign() <= 0 {
		return
	}
	value := bigToFloat(amount)
	if value <= 0 {
		return
	}
	m.rewardsPaid.Add(value)
}

// SetPaused toggles the staking pause indicator gauge.
func (m *StakingMetrics) SetPaused(paused bool) {
	if m == nil {
		return
	}
	if paused {
		m.paused.Set(1)
		return
	}
	m.paused.Set(0)
}

// RecordTotalStaked updates the gauge tracking staked ZapNHB per account.
func (m *StakingMetrics) RecordTotalStaked(account string, amount *big.Int) {
	if m == nil {
		return
	}
	label := strings.TrimSpace(strings.ToLower(account))
	if label == "" {
		label = "unknown"
	}
	value := bigToFloat(amount)
	if value < 0 {
		value = 0
	}
	m.totalStaked.WithLabelValues(label).Set(value)
}

// RecordCapHit increments the counter tracking emission cap saturations.
func (m *StakingMetrics) RecordCapHit() {
	if m == nil {
		return
	}
	m.capHit.Inc()
}

// RecordIndexPersistFailure increments the counter tracking failed staking index persistence attempts.
func (m *StakingMetrics) RecordIndexPersistFailure() {
	if m == nil {
		return
	}
	m.indexPersistFailures.Inc()
}

// IndexPersistFailureCounter exposes the persistence failure counter for testing.
func (m *StakingMetrics) IndexPersistFailureCounter() prometheus.Counter {
	if m == nil {
		return nil
	}
	return m.indexPersistFailures
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
