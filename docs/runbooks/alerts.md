# Alert Runbook

This runbook covers the two SLO alerts defined in `ops/prometheus/rules/slo.rules.yml`.
Both rules are computed from the OpenTelemetry span metrics that the collector in
`ops/otel/collector.yaml` produces (`spanmetrics_calls_total`, `spanmetrics_error_count`
and `spanmetrics_latency_bucket`), grouped by the `service_name` label.

The Prometheus configuration in `ops/prometheus/prometheus.yml` scrapes the
`nhb-services` job (path `/metrics`, port `9464`) for the targets listed there, and
loads the rule file above. The Grafana dashboard `NHB Services Overview`
(`ops/grafana/dashboards/services-overview.json`) has the panels named below.

## ServiceErrorBudgetBurn

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

## ServiceLatencyRegression

**Rule**: `slo:service_latency_p95:5m > 0.75` (seconds) for `10m`, severity label `warn`.

The recorded series `slo:service_latency_p95:5m` is
`histogram_quantile(0.95, sum(rate(spanmetrics_latency_bucket[5m])) by (service_name, le))`.

**Dashboard**: `NHB Services Overview`, panel `5m p95 Request Latency`.

**Checks**:
1. Confirm the regression on the panel above for the affected `service_name`.
2. Find the slow spans in Tempo for that service.
3. Check host resources (CPU, memory, disk) for saturation.

## Related signals

The gateway also exposes request metrics (see `gateway/middleware/observability.go`) and
the consensus node exposes the POS and paymaster metrics listed in
[POS SLA and troubleshooting](./pos-slas.md) and
[Paymaster budget](./paymaster-budgets.md).
