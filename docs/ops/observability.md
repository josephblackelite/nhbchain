# Observability Stack

This document describes how the NHB Chain observability stack is deployed, how data flows through the system, and how to operate the dashboards and alerts that keep the network healthy.

## Metrics

### Collection
- **Prometheus** scrapes the `/metrics` endpoint on every validator, RPC node, and oracle service.
- Exporters: `prometheus-node-exporter` for host-level stats, custom NHB Chain exporters for consensus and RPC metrics, and the OpenTelemetry collector's Prometheus exporter for application metrics.
- Metrics are labeled with `cluster`, `role`, `shard`, and `env` to enable granular dashboards.
- The Prometheus configuration is versioned at [`ops/prometheus/prometheus.yml`](../../ops/prometheus/prometheus.yml) with SLO recording and alerting rules in [`ops/prometheus/rules/slo.rules.yml`](../../ops/prometheus/rules/slo.rules.yml).
- Module-level request instrumentation is exposed via the `nhb_module_*` Prometheus series to track per-method QPS, latency, and throttle pressure.

### Key Metrics & Thresholds
- **Consensus health**: `nhb_consensus_finality_lag_seconds` should remain < 15s; alert at 30s.
- **RPC latency**: `nhb_rpc_request_duration_seconds` 95th percentile < 500ms; alert at 1s.
- **Module health**: `nhb_module_requests_total` error outcome < 5% and `nhb_module_request_duration_seconds` p95 < 1s per module.
- **Throttle pressure**: `nhb_module_throttles_total` increases > 25/5m per module or any `reason="quota"` increment.
- **Block production**: `nhb_validator_blocks_signed_total` should increase every epoch; alert if flat for > 2 epochs.
- **Oracle freshness**: `nhb_oracle_update_age_seconds` < 60s; alert at 120s.

### Dashboards
- **Network Overview**: per-cluster latency, throughput, and finality trends.
- **Validator Drill-down**: CPU, memory, disk I/O, and consensus participation for each validator.
- **RPC Performance**: request throughput, error rates, cache hit ratio, HMAC auth failures.
- **Oracle Health**: feed latency, signer distribution, on-chain submission success.
- **Services Overview**: error budget burn, p95 latency, and throughput per service sourced from the spanmetrics connector (see [`ops/grafana/dashboards/services-overview.json`](../../ops/grafana/dashboards/services-overview.json)).
- **Staking Health**: emissions cadence, bonded supply, pause state, and emission-cap pressure for the staking module (see [`observability/grafana/staking.json`](../../observability/grafana/staking.json)).
- **Loyalty Budget**: proration posture, queued demand, and daily payout trend so operators can correlate emission throttling with the configured caps.

#### Staking Health Dashboard

The staking dashboard focuses on the four telemetry signals operations teams need to keep validator incentives healthy:

- **Rewards Paid per Day** visualises `nhb_staking_rewards_paid_zn_total` converted to ZNHB with a daily window. The panel should jump on payout days—flat lines indicate missed payouts, while spikes beyond expectations suggest runaway emissions.
- **Total Staked ZNHB** aggregates the `nhb_staking_total_staked{account="…"}` gauges into a single timeseries. Track this for sudden drawdowns that could precede churn or validator instability.
- **Staking Pause Status** reflects `nhb_staking_paused`. `0` (green) means delegation, undelegation, and reward claims are accepted; `1` (red) means the module is administratively frozen and all mutations will return `codeModulePaused` until governance clears the pause.
- **Emission Cap Hits** counts `nhb_staking_cap_hit_total`. The stat remains green at zero, turns yellow on the first cap exhaustion, and red once multiple hits accumulate—those events require treasury coordination before the next payout.

Pair these panels with alerting on the `Emission Cap Hits` and pause flag so operators are paged when the module halts or emissions saturate.

#### Loyalty Budget Dashboard

Use the loyalty dashboard to understand when the pro-rate guardrail is active and how quickly the treasury budget is being consumed:

- **Budget Remaining (`loyalty_budget_zn`)** tracks the ZNHB still available to issue for the current UTC day. Sudden drops without matching payouts may indicate configuration drift or a stale fee window.
- **Queued Demand (`loyalty_demand_zn`)** mirrors the pending payout total collected at `EndBlockRewards`. Rising demand with a flat budget hints that future blocks will be prorated.
- **Prorate Ratio (`loyalty_prorate_ratio`)** exposes the applied multiplier (1.0 means 100% payout). Values below `1` confirm that pro-rate mode has engaged and the ratio reflected in the `LoyaltyBudgetProRated` event was emitted.
- **Paid Today (`loyalty_paid_today_zn`)** increments as payouts land. The series resets on the UTC day boundary; if it fails to reset, inspect the day-rollover cron or block timestamps.

When `loyalty_prorate_ratio` drops under `1`, correlate the timestamp with `LoyaltyBudgetProRated` events and treasury balances to validate that proration is expected and not the result of price-guard failures.

## Tracing

### Instrumentation
- Services emit OpenTelemetry traces with span attributes for `request_id`, `client_id`, and `txn_hash`.
- All gRPC servers register the `otelgrpc` unary and stream interceptors so that trace IDs, baggage, and relevant RPC attributes are captured automatically.
- gRPC and HTTP clients use `otelgrpc`/`otelhttp` instrumentation so trace context flows through downstream calls with no manual propagation.
- RPC nodes and the HTTP gateway forward W3C Trace Context (`traceparent`, `tracestate`) headers on every proxied request to keep cross-service traces stitched together.
- Sample rate defaults to 10% for production, 100% for staging to aid debugging and is controlled via the collector configuration.

### Export & Storage
- **OTLP/HTTP** exporter ships traces to Tempo, retained for 72 hours.
- Derived metrics for trace errors feed into Prometheus via the spanmetrics connector.
- Sensitive values (`account_number`, `auth_token`) must be redacted before spans are exported; the collector removes these attributes before export.
- The canonical configuration lives in [`ops/otel/collector.yaml`](../../ops/otel/collector.yaml); update it when adding receivers, exporters, or attribute processors.

### Dashboards & Usage
- Use Grafana Explore with the Tempo data source to follow requests across RPC, consensus, and storage services.
- Trace exemplars are linked from latency panels in the RPC dashboard and the Services Overview latency panel.
- The [`examples/compose/observability.yml`](../../examples/compose/observability.yml) stack provisions Prometheus, Tempo, Loki, and Grafana locally with the NHB dashboards and data sources for quick validation.

## Structured Logging

### Format & Routing
- Logs are JSON with fields `timestamp`, `severity`, `service`, `env`, `request_id`, and `message`.
- PII is hashed or dropped at the source. Access tokens and HMAC secrets are never logged.
- Fluent Bit forwards logs to Loki with retention of 14 days.

### Searching & Alerts
- LogQL dashboards surface error spikes, failed signature verifications, and oracle stale data messages.
- Critical log patterns (e.g., `validator_missed_signature`, `kms_rotation_failed`) generate alerts routed through Alertmanager.

## Alerting

### Routing
- Alertmanager routes incidents by severity:
  - **P1**: paging SRE on-call via PagerDuty and #sre-alerts.
  - **P2**: notify ops triage channel and create Jira ticket.
  - **P3**: email weekly digest to platform team.
- Alerts include RACI contacts and runbook links.

### Policy Highlights
- **Finality lag**: triggered when lag > 30s for 3 consecutive intervals.
- **RPC failure rate**: triggered at > 2% error rate over 10 minutes.
- **Module high error rate**: `ModuleHighErrorRate` fires after 10 minutes above the 5% budget per module.
- **Module latency regression**: `ModuleLatencyP95Degraded` warns when the p95 exceeds 1s for 15 minutes.
- **Module throttles**: `ModuleThrottleSaturation`, `ModulePauseEngaged`, and `ModuleQuotaExhausted` surface rate-limit backpressure, pause guards, and quota exhaustion.
- **Oracle stale data**: triggered when data age > 2 minutes.
- **Validator downtime**: triggered when heartbeat missing for 3 epochs.

## Operations

### Access Control
- Use least-privilege Prometheus and Grafana API tokens scoped per environment.
- Rotate tokens quarterly or immediately after staff changes.

### Incident Readiness
- Quarterly alert routing drills verify contact accuracy.
- Dashboards and alert definitions are version-controlled and peer reviewed.

## Validation Checklist
- [ ] Metrics endpoints respond with `200 OK` and expected labels.
- [ ] Grafana dashboards display live data for consensus, RPC, oracle, and storage services.
- [ ] Tempo contains traces linked to Grafana panels.
- [ ] Alertmanager test notifications reach the correct channels.
