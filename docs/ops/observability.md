# Observability Stack

This document describes how the NHB Chain observability stack is deployed, how data flows through the system, and how to operate the dashboards and alerts that keep the network healthy.

## Metrics

### Collection
- **Prometheus** scrapes the `/metrics` endpoint on every validator, RPC node, and oracle service.
- Exporters: `prometheus-node-exporter` for host-level stats, custom NHB Chain exporters for consensus and RPC metrics.
- Metrics are labeled with `cluster`, `role`, `shard`, and `env` to enable granular dashboards.

### Key Metrics & Thresholds
- **Consensus health**: `nhb_consensus_finality_lag_seconds` should remain < 15s; alert at 30s.
- **RPC latency**: `nhb_rpc_request_duration_seconds` 95th percentile < 500ms; alert at 1s.
- **Block production**: `nhb_validator_blocks_signed_total` should increase every epoch; alert if flat for > 2 epochs.
- **Oracle freshness**: `nhb_oracle_update_age_seconds` < 60s; alert at 120s.

### Dashboards
- **Network Overview**: per-cluster latency, throughput, and finality trends.
- **Validator Drill-down**: CPU, memory, disk I/O, and consensus participation for each validator.
- **RPC Performance**: request throughput, error rates, cache hit ratio, HMAC auth failures.
- **Oracle Health**: feed latency, signer distribution, on-chain submission success.

## Tracing

### Instrumentation
- Services emit OpenTelemetry traces with span attributes for `request_id`, `client_id`, and `txn_hash`.
- RPC nodes propagate distributed trace context across internal microservices via W3C Trace Context headers.
- Sample rate defaults to 10% for production, 100% for staging to aid debugging.

### Export & Storage
- **OTLP/HTTP** exporter ships traces to Tempo, retained for 72 hours.
- Derived metrics for trace errors feed into Prometheus via the spanmetrics connector.
- Sensitive values (`account_number`, `auth_token`) must be redacted before spans are exported.

### Dashboards & Usage
- Use Grafana Explore with the Tempo data source to follow requests across RPC, consensus, and storage services.
- Trace exemplars are linked from latency panels in the RPC dashboard.

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
