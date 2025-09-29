# Stable cash-out observability

This guide captures the dashboards, metrics, and alert responses for the stable voucher → payout pipeline.

## Metrics

The following Prometheus series are emitted by the services that participate in the pipeline:

| Metric | Service | Labels | Description |
| --- | --- | --- | --- |
| `nhb_oracle_attesterd_voucher_rate` | `oracle-attesterd` | `asset` | Counter of deposit vouchers minted after webhook validation. |
| `nhb_oracle_attesterd_oracle_freshness_seconds` | `oracle-attesterd` | `asset` | Age in seconds between the upstream webhook timestamp and the successful mint. |
| `nhb_swapd_stable_requests_total` | `swapd` | `operation`, `outcome` | Count of quote, reserve, and cash-out intent requests observed by the experimental stable engine. |
| `nhb_swapd_stable_request_duration_seconds` | `swapd` | `operation` | Histogram tracking the latency of each stable engine operation. |
| `nhb_payoutd_payout_latency_seconds` | `payoutd` | `asset` | Histogram of end-to-end payout latency from processor admission to attestation submission. |
| `nhb_payoutd_cap_remaining` | `payoutd` | `asset` | Remaining soft cap (in integer stable units) for the current window. |
| `nhb_payoutd_cap_utilization` | `payoutd` | `asset` | Ratio of cap consumed in the current window. |
| `nhb_payoutd_errors_total` | `payoutd` | `asset`, `reason` | Counter of payout failures segmented by error reason. |
| `nhb_payoutd_pause_engaged` | `payoutd` | — | Gauge that is `1` when the processor pause guard is enabled. |

### Tracing

OpenTelemetry spans now cover the two critical paths:

* `swapd/stable` emits spans for quote, reserve, and cash-out intent creation so traces show the quote → reserve → credit transition.
* `payoutd/processor` emits spans for processing intents, wallet transfers, confirmation polling, and receipt attestation to capture the intent → payout → receipt lifecycle.

Each span carries the relevant IDs (quote, reservation, intent, transaction hash) to make correlating traces with logs straightforward.

## Dashboards

Add the following panels to the **Stable Cash-Out** Grafana dashboard:

* **Voucher Mint Rate** – plot `rate(nhb_oracle_attesterd_voucher_rate[5m])` by `asset`.
* **Oracle Freshness** – graph `max by (asset) (nhb_oracle_attesterd_oracle_freshness_seconds)` with a 120s threshold line.
* **Payout Latency (p95)** – use `histogram_quantile(0.95, sum(rate(nhb_payoutd_payout_latency_seconds_bucket[5m])) by (asset, le))`.
* **Cap Remaining vs Utilisation** – dual panel showing `nhb_payoutd_cap_remaining` and `nhb_payoutd_cap_utilization` by `asset`.
* **Stable Engine Throughput** – table of `increase(nhb_swapd_stable_requests_total[1h])` by `operation`/`outcome` for quick smoke checks.

## Alerts

The following alerts were added to `observability/alerts.yaml`:

| Alert | Condition | Owner | Response |
| --- | --- | --- | --- |
| `OracleAttestationFreshness` | `max(nhb_oracle_attesterd_oracle_freshness_seconds) > 120s` for 5m | Data | Check oracle-attesterd logs for stuck webhooks. Verify NowPayments webhook delivery and consensus submissions. |
| `PayoutdErrorSpike` | `sum(increase(nhb_payoutd_errors_total[5m])) > 3` for 10m | Treasury | Inspect payoutd logs for `transfer`, `confirmations`, or `attest` failures. Coordinate with treasury ops if on-chain congestion persists. |
| `PayoutdLatencyP95Breached` | p95 latency above 30s for 15m | Treasury | Review wallet RPC health and blockchain congestion. Pause payouts if latency keeps increasing. |
| `PayoutdCapUtilizationHigh` | `max_over_time(nhb_payoutd_cap_utilization[10m]) > 0.8` for 10m | Treasury | Refill the soft balance or raise the daily cap; communicate expected impact to finance. |
| `PayoutdPauseEngaged` | `max_over_time(nhb_payoutd_pause_engaged[5m]) >= 1` for 5m | Treasury | Confirm whether the pause is intentional. Follow the incident checklist to resume service safely. |

## Runbooks

1. **Oracle freshness degraded**
   * Check the **Voucher Mint Rate** panel for drops. If the rate is zero, confirm webhook delivery with the payment provider.
   * Review `oracle-attesterd` logs for the corresponding `voucher minted` entries to see if consensus submissions are failing.
   * If the service is healthy, escalate to the integration team to re-send the stuck invoices.

2. **Payout latency regression**
   * Inspect the Grafana latency panel to confirm whether the regression is limited to a single asset.
   * Review `payoutd` logs for `payout settled` entries and measure the time between transfer, confirmation, and attestation spans.
   * Coordinate with treasury to pause payouts (`admin/pause`) if the backlog keeps growing, then resume once on-chain congestion clears.

3. **Cap nearly exhausted**
   * Check `nhb_payoutd_cap_remaining` for the affected asset.
   * Reconcile soft inventory with finance and adjust the policy YAML if additional headroom is needed.
   * Notify stakeholders about the expected time to cap reset and whether manual minting is required.

These runbooks must be exercised in staging before pushing changes to production. Validate that `/metrics` on each service exposes the new series (`voucher_rate`, `oracle_freshness_seconds`, `cap_utilization`, `pause_engaged`, and the updated payout latency histogram) using either localnet smoke tests or replay scripts.
