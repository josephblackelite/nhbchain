# Performance Baselines

Use this document to record the throughput and latency expectations for nhbchain components. Operators should track these metrics continuously to detect regressions during releases.

## Consensus

| Metric | Target | Measurement |
| --- | --- | --- |
| Block time | 2.5s ± 0.5s | `consensus.block_interval_seconds` Prometheus gauge |
| Transactions per second | 300 TPS sustained | `consensus.tx_applied_total` rate |
| Finality lag | < 5s | Difference between block time and `consensus.finalized_height` timestamps |

## Gateway

| Metric | Target | Measurement |
| --- | --- | --- |
| API p95 latency | < 250ms | `http_server_request_duration_seconds` histogram |
| Order ingest rate | 100 rps sustained | `gateway.orders_ingested_total` rate |
| Voucher mint SLA | < 60s | Difference between request timestamp and mint confirmation |

## OTC Reconciler

| Metric | Target | Measurement |
| --- | --- | --- |
| Reconciliation duration | < 10 minutes per 24h window | Scheduler logs and `recon_run_duration_seconds` metric |
| Anomaly rate | < 1% of invoices | Ratio of flagged rows to total invoices |

## Validator operations

| Metric | Target | Measurement |
| --- | --- | --- |
| State sync duration | < 30 minutes from snapshot | Operator logs and `consensus.statesync_duration_seconds` |
| Gossip peers | ≥ 30 stable peers | `p2p_peer_count` gauge |
| CPU utilization | < 75% sustained | Node exporter metrics |

## Tracking and alerts

- Store baseline values in Grafana dashboards tagged `baseline:true`.
- Configure alert rules to trigger when metrics exceed ±20% of baseline for two consecutive evaluation periods.
- Re-baseline after major releases or infrastructure changes and update this document with the new targets.
