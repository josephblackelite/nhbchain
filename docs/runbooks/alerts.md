# Alert Runbook

The repository ships two alert files:

* `ops/prometheus/rules/slo.rules.yml`: two service-level alerts computed from
  OpenTelemetry span metrics (described first below).
* `observability/alerts.yaml`: rules on the `nhb_*` metrics the `nhb` and `consensusd`
  processes register, including the consensus liveness alerts (see
  [Consensus liveness alerts](#consensus-liveness-alerts)).

## Service SLO alerts

Both rules in `ops/prometheus/rules/slo.rules.yml` are computed from the OpenTelemetry span
metrics that the collector in `ops/otel/collector.yaml` produces (`spanmetrics_calls_total`,
`spanmetrics_error_count` and `spanmetrics_latency_bucket`), grouped by the `service_name`
label.

The Prometheus configuration in `ops/prometheus/prometheus.yml` scrapes the
`nhb-services` job (path `/metrics`, port `9464`) for the targets listed there, and
loads the rule file above. No Go code in this repository binds port `9464`; in
`ops/otel/collector.yaml` that port is the collector's own Prometheus exporter. The
Grafana dashboard `NHB Services Overview`
(`ops/grafana/dashboards/services-overview.json`) has the panels named below.

### ServiceErrorBudgetBurn

**Rule**: `slo:service_error_ratio:5m > 0.02` for `10m`, severity label `page`.

The recorded series `slo:service_error_ratio:5m` is
`sum(rate(spanmetrics_error_count[5m])) / sum(rate(spanmetrics_calls_total[5m]))`,
per `service_name`.

**Dashboard**: `NHB Services Overview`, panel `5m Error Budget Consumption`.

**Checks**:
1. Query `spanmetrics_calls_total` and `spanmetrics_error_count` for the affected
   `service_name` to confirm the ratio.
2. Look at recent deployments or configuration changes for that service.
3. Inspect the service's request logs. The collector's logs pipeline exports to Loki
   (`ops/otel/collector.yaml`) and its traces pipeline exports to Tempo.

### ServiceLatencyRegression

**Rule**: `slo:service_latency_p95:5m > 0.75` (seconds) for `10m`, severity label `warn`.

The recorded series `slo:service_latency_p95:5m` is
`histogram_quantile(0.95, sum(rate(spanmetrics_latency_bucket[5m])) by (service_name, le))`.

**Dashboard**: `NHB Services Overview`, panel `5m p95 Request Latency`.

**Checks**:
1. Confirm the regression on the panel above for the affected `service_name`.
2. Find the slow spans in Tempo for that service.
3. Check host resources (CPU, memory, disk) for saturation.

## Consensus liveness alerts

The `consensus-liveness` group in `observability/alerts.yaml` reads metrics that the `nhb`
and `consensusd` processes register (`observability/metrics.go`). Those metrics are served
on `/metrics` only when the environment variable `NHB_METRICS_ADDR` is set to a listen
address (`observability/metrics_server.go`); with it unset there is no listener. The
listener is unauthenticated, so bind it to loopback or a private interface. `cmd/p2pd` does not
start it.

| Alert | Expression | For | Severity |
| --- | --- | --- | --- |
| `ConsensusNoCommit` | `nhb_consensus_seconds_since_last_commit > 60` | 1m | critical |
| `ConsensusBuildFailing` | `nhb_consensus_build_failures_consecutive >= 2` | 30s | critical |
| `ConsensusEmptyBlockFallbackRate` | `increase(nhb_consensus_empty_block_fallbacks_total[5m]) > 20` | 0m | warning |
| `MempoolNondeterministicTransactions` | `increase(nhb_mempool_tx_failures_total{disposition="nondeterministic"}[10m]) > 0` | 0m | warning |
| `ConsensusTxPanicRecovered` | `increase(nhb_consensus_tx_panics_recovered_total[10m]) > 0` | 0m | warning |

When one fires, search the validator log for lines that start with `LIVENESS:`. The
[validator runbook](../ops/validator-runbook.md#block-production-liveness) lists each line and its
structured fields. The metric `nhb_consensus_block_interval_seconds` is the interval between
the timestamps of consecutive committed blocks; how the pace of an idle chain is set is in
[Block cadence](../consensus/block-cadence.md).

The same file also defines the `module-runtime` group, which alerts on module error rate,
module p95 latency, throttle counts, oracle update age, and the paymaster auto top-up
counters (see
[Paymaster automatic top-up](./paymaster-autotopup.md)).

## Related signals

The gateway also exposes request metrics (see `gateway/middleware/observability.go`) and
the consensus node exposes the POS and paymaster metrics listed in
[POS SLA and troubleshooting](./pos-slas.md) and
[Paymaster budget](./paymaster-budgets.md).
