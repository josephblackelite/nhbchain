# Observability

What the code in this repository emits, and the sample configuration for
collecting it. Sources: `observability/`, `gateway/middleware/observability.go`,
`ops/`, `examples/compose/observability.yml`.

## OpenTelemetry export

`cmd/consensusd`, `cmd/p2pd`, `cmd/gateway`, `services/governd`,
`services/lendingd`, `services/lending`, `services/identity-gateway`
and `services/escrow-gateway` call `telemetry.Init`
(`observability/otel/init.go`). It sets up OTLP over HTTP for both traces and
metrics:

| Variable | Effect |
| -------- | ------ |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `host:port` of the OTLP/HTTP receiver. Default `localhost:4318`. |
| `OTEL_EXPORTER_OTLP_HEADERS` | Comma-separated `key=value` headers. |
| `OTEL_EXPORTER_OTLP_INSECURE` | Boolean; plaintext is used unless this is set to a false value (default true). |
| `NHB_ENV` | Recorded as the `deployment.environment` resource attribute. |

Traces are batched (2 s timeout, 512 spans per batch) and metrics are pushed every
15 s. No sampler is configured, so the OpenTelemetry SDK default applies. The
propagators are W3C Trace Context and Baggage. The gRPC servers use the
`otelgrpc` interceptors; the gateway proxies inject trace context into upstream
requests.

## Prometheus metrics defined in Go

`observability/metrics.go`, `observability/metrics/potso.go`, `network/metrics.go`
and `p2p/metrics.go` register these collectors on the default Prometheus
registry. The only listener in this repository that serves a Prometheus endpoint
is the gateway (below); no node, consensus or p2p binary mounts a `/metrics`
handler.

| Metric | Labels |
| ------ | ------ |
| `nhb_module_requests_total` | `module`, `method`, `outcome` (`success` or `error`) |
| `nhb_module_errors_total` | `module`, `method`, `status` |
| `nhb_module_request_duration_seconds` | `module`, `method` |
| `nhb_module_throttles_total` | `module`, `reason` (only `rate_limit` is recorded by the RPC server) |
| `nhb_rpc_limiter_hits_total` | `scope`, `module`, `route` |
| `nhb_security_insecure_binds_total` | `service`, `loopback` |
| `nhb_token_supply_total` | `token` |
| `nhb_consensus_block_interval_seconds` | none |
| `nhb_mempool_pos_lane_fill`, `nhb_mempool_pos_lane_backlog{asset}`, `nhb_mempool_pos_tx_enqueued_total`, `nhb_mempool_pos_p95_finality_ms` | |
| `nhb_pos_auth_expired_total` | none |
| `nhb_paymaster_autotopups_total{outcome}`, `nhb_paymaster_autotopup_amount_wei_total{outcome}` | |
| `nhb_loyalty_budget_zn`, `nhb_loyalty_demand_zn`, `nhb_loyalty_prorate_ratio`, `nhb_loyalty_paid_today_zn` | none |
| `nhb_loyalty_price_fallback_total` | `strategy` |
| `nhb_staking_rewards_paid_zn`, `nhb_staking_paused`, `nhb_staking_cap_hit`, `nhb_staking_index_persist_failures_total` | none |
| `nhb_staking_total_staked` | `account` |
| `nhb_events_transfers_total` | `asset` |
| `nhb_network_relay_queue_enqueued_total`, `nhb_network_relay_queue_dropped_total`, `nhb_network_relay_queue_occupancy` | none |
| `nhb_p2p_peer_score`, `nhb_p2p_peer_latency_ms`, `nhb_p2p_peer_useful_events`, `nhb_p2p_peer_misbehavior` | `peer` |
| `nhb_p2p_handshakes_total` | `result` |
| `nhb_p2p_gossip_messages_total` | `direction`, `type` |
| `nhb_swapd_stable_requests_total`, `nhb_swapd_stable_request_duration_seconds`, `nhb_swapd_stable_errors_total` | `operation`, `outcome` / `operation` / `operation`, `reason` |
| `potso_evidence_accepted_total`, `potso_penalty_applied_total` | `type` |
| `potso_epoch_pool` | none |
| `potso_rewards_sum`, `potso_rounding_dust`, `potso_heartbeat_unique_peers`, `potso_heartbeat_avg_session_seconds` | `epoch` |
| `potso_heartbeat_total`, `potso_heartbeat_rate_limited_total`, `potso_heartbeat_wash_total` | `epoch`, `address` |
| `potso_webhook_failures_total` | `destination` |

`nhb_loyalty_prorate_ratio` is `1.0` for a full payout and lower while
pro-rating applies (`Loyalty()` in `observability/metrics.go`).
`nhb_staking_paused` is `1` when staking mutations are paused.

The escrow gateway records one OpenTelemetry counter,
`nhb.escrow.webhooks.dropped` (attribute `reason`); see
[Escrow gateway webhook queue operations](./webhooks.md).

### Gateway endpoint

`GET /metrics` on the gateway serves its own registry: `<metricsPrefix>_requests_total{route,method,status}`
and `<metricsPrefix>_request_duration_seconds{route,method}` (prefix default
`gateway`). See [Gateway Overview](../gateway/overview.md#observability).

## Sample collector and Prometheus configuration

- `ops/otel/collector.yaml`: OTLP receivers on `0.0.0.0:4317` (gRPC) and
  `0.0.0.0:4318` (HTTP); a Prometheus receiver scraping `gateway`, `consensusd`,
  `p2pd`, `governd`, `lendingd` and `swapd` on port `9464`; processors
  `memory_limiter` and `batch`, and `attributes/redact` which deletes the span
  attributes `account_number` and `auth_token`; the `spanmetrics` connector
  (dimensions `service.name`, `rpc.system`, `rpc.service`, `http.method`,
  `http.route`); exporters `otlp/tempo` (`tempo:4318`), `prometheus` (`0.0.0.0:9464`,
  namespace `nhb`), `loki` and `logging`. Trace pipeline: `otlp` receiver, exporters
  `otlp/tempo`, `spanmetrics`, `logging`.
- `ops/prometheus/prometheus.yml`: 15 s scrape and evaluation intervals,
  external label `cluster: nhb-local`, jobs `otel-collector` (`otel-collector:8888`)
  and `nhb-services` (the same six hosts on `:9464`), and the rule file
  `ops/prometheus/rules/slo.rules.yml`.
- `examples/compose/observability.yml`: Tempo, Loki, the OpenTelemetry Collector
  (contrib image, ports 4317, 4318, 9464, 13133), Prometheus (port 9090) and
  Grafana (port 3000) using the two files above and `ops/grafana/dashboards`.

The sample Prometheus configuration targets `<service>:9464`, which is the
collector's Prometheus exporter port in `collector.yaml`; the services themselves
do not listen on 9464.

## Alert rules in the repository

`ops/prometheus/rules/slo.rules.yml` records `slo:service_error_ratio:5m` and
`slo:service_latency_p95:5m` from the spanmetrics series and defines
`ServiceErrorBudgetBurn` (error ratio above 2% for 10 minutes) and
`ServiceLatencyRegression` (p95 above 750 ms for 10 minutes).

`observability/alerts.yaml`:

| Alert | Expression summary |
| ----- | ------------------ |
| `ModuleHighErrorRate` | error outcome above 5% of `nhb_module_requests_total` per module for 10 min |
| `ModuleLatencyP95Degraded` | p95 of `nhb_module_request_duration_seconds` above 1 s for 15 min |
| `ModuleThrottleSaturation` | more than 25 `nhb_module_throttles_total` increases in 5 min for 5 min |
| `ModulePauseEngaged` | `nhb_module_throttles_total{reason="pause"}` changed within 1 h |
| `ModuleQuotaExhausted` | `nhb_module_throttles_total{reason="quota"}` increased in 10 min |
| `OracleFeedStale` | `max(nhb_oracle_update_age_seconds) > 120` for 5 min |
| `PaymasterAutoTopUp*` | four alerts on `nhb_paymaster_autotopups_total` (success spikes above 3 and 10 in 5 min; failures) |

`observability/alerts/alert_rules.yaml` holds the `POTSO*` alerts and
`TokenSupplyJumpIncrease` / `TokenSupplyJumpDecrease`.

## Dashboards in the repository

- `ops/grafana/dashboards/services-overview.json` ("NHB Services Overview"): error
  budget, p95 latency, throughput (from the spanmetrics series) and token supply.
- `ops/grafana/dashboards/fees.json` ("NHB Fee Transparency").
- `observability/grafana/staking.json` and `observability/dashboards/staking.json`
  (staking), `observability/dashboards/paymaster-autotopup.json`, and the POTSO
  dashboards in `observability/grafana/dashboards/`.

## Logging

`logging.Setup` (`observability/logging/logging.go`) writes JSON to stdout with
the fields `timestamp`, `severity`, `message`, `service` and, when `NHB_ENV` is
set, `env`. If `NHB_LOG_FILE` is set, output is also written to that file with
rotation (100 MB per file, 5 backups, 28 days, compressed). It also redirects the
standard-library `log` output through the same handler. `observability/logging/redact.go`
provides redaction helpers.
