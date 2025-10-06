# POS SLA & Troubleshooting Runbook

This runbook enumerates the service-level objectives for POS sponsorship, associated observability signals, and remediation steps when quality-of-service degrades. The real-time metrics exported by the consensus node underpin the dashboards and alerts described below.【F:observability/metrics.go†L185-L236】【F:docs/specs/pos-qos.md†L16-L63】

## 1. Service level objectives

| KPI | Target | Data source |
| --- | --- | --- |
| Finality latency (p95) | ≤ 3.5s enqueue → finality | `pos_p95_finality_ms` histogram on the consensus metrics endpoint.【F:observability/metrics.go†L189-L197】 |
| Gateway acceptance rate | ≥ 99.5% | Gateway ingestion logs and `pos_gateway_rejections_total` counter. |
| Sponsored throughput headroom | ≥ 20% above rolling 7d average | Paymaster burn-rate panel in Grafana. |

Failure to meet any KPI for 10 consecutive minutes should trigger on-call escalation.

## 2. Monitoring workflow

1. **Dashboards** – The `POS Operations` Grafana board combines finality latency, gateway acceptance, and paymaster utilisation traces. Filter by merchant to isolate outliers.
2. **Logs** – Stream gateway logs for error spikes: `kubectl logs deploy/gateway -f | grep pos`.
3. **Realtime feed** – Use the realtime finality WebSocket `/ws/pos/finality` to observe individual transaction progress during incidents.【F:docs/api/pos-realtime.md†L14-L64】【F:rpc/http.go†L328-L336】

## 3. QoS tuning

1. **Gateway queue reservation**
   * Adjust the POS queue reservation if latency drifts up: update the gateway config map and reload the deployment (`kubectl rollout restart deploy/gateway`).
   * Reference the QoS specification for recommended reservation ratios across busy periods.【F:docs/specs/pos-qos.md†L20-L63】
2. **Consensus tuning**
   * Validate validator vote participation; if `nhb_consensus_finality_lag_seconds` rises above 15s, investigate validator health.
   * Increase proposer batch size only if mempool saturation is observed; confirm with `make bugcheck-perf` in staging before deploying changes.【F:scripts/bugcheck.sh†L118-L128】
3. **Paymaster throttles**
   * Review `PaymasterLimits` in the node config if devices are being throttled unexpectedly.【F:core/sponsorship.go†L92-L145】【F:config/global.go†L8-L56】

## 4. Common incidents & mitigation

| Symptom | Likely cause | Resolution |
| --- | --- | --- |
| Finality latency > 3.5s | Validator offline or consensus backlog | Page validator owners, restart unhealthy nodes, run `make bugcheck-perf` in canary to validate chain health. |
| Gateway rejections spike | Merchant paused or device revoked | Check registry change log, coordinate with risk, and resume if appropriate. |
| Sponsored cap exhausted mid-day | Caps misconfigured or merchant surge | Follow the paymaster budget runbook to evaluate cap increase and document the change. |
| Device attestation failures | Expired certificates or firmware mismatch | Execute the device attestation runbook to reissue credentials. |

## 5. Incident review checklist

1. Capture dashboard snapshots (finality, acceptance, budgets) and attach to the incident ticket.
2. Export relevant log segments and mTLS handshake traces for forensic analysis.
3. File a post-incident review summarising impact, timeline, and corrective actions.
4. Raise follow-up tasks for tooling gaps (e.g., automate cap alerts, improve dashboard annotations).
