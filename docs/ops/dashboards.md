# OTC Operations Dashboards

This document outlines recommended dashboard widgets and alerting strategies based on the OTC Operations console metrics and workflows.

## 1. Executive Overview Panel

- **Invoice Volume (Gauge):** Visualize `otc_invoice_volume_total` to understand active throughput.
- **Cap Utilization (Gauge):** Display `otc_cap_usage_ratio` with 0.8 and 0.95 thresholds highlighted.
- **Mint Success Counter:** Track cumulative `otc_mint_success_total`.

## 2. Workflow Health

- **Approval Latency Histogram:** Plot `otc_approval_latency_seconds` with P50/P90 overlays. Alert when P90 exceeds 6 hours.
- **Stage Distribution Table:** Use API `/api/invoices` grouped by `stage` to show counts per queue.
- **Receipt Upload Failures:** Graph `otc_receipt_upload_failures_total` deltas with alert at >3 per hour.

## 3. Branch Operations Drilldown

- **Branch Filtered Views:** For each branch, filter `otc_invoice_volume_total` by branch tag (extend metrics labels when integrating with backend).
- **Due Today Heatmap:** Use UI export filtered by `receiptDueAt` to feed scheduled reminders.
- **Escalation Timeline:** Visualize timeline events (API data) to highlight repeated escalations.

## 4. Signer Health & On-Chain Monitoring

- **Signer Health Gauge:** Chart `otc_signer_health` (0/1). Trigger page/Slack when drops to 0.
- **Voucher Submission Feed:** Pull webhook payloads into log dashboard to audit `txHash` activity.
- **Mint Success Trend:** Graph 24h rolling sum of `otc_mint_success_total` increases.

## 5. Alerting Recommendations

| Metric | Condition | Action |
| ------ | --------- | ------ |
| `otc_cap_usage_ratio` | > 0.9 for 10 min | Page treasury capacity owner |
| `otc_approval_latency_seconds` | P95 > 4 hours | Open incident ticket |
| `otc_receipt_upload_failures_total` | Rate > 5 / hr | Notify branch ops channel |
| `otc_signer_health` | = 0 | Escalate to SuperAdmin |

## 6. Data Export Workflows

- Schedule CSV exports via `/api/invoices/export` for daily reconciliation.
- Archive webhook payloads to create immutable audit logs for each lifecycle event.
- Integrate metrics scrape with existing Prometheus + Grafana stack.

## 7. Future Enhancements

- Add metric labels for `branch`, `stage`, and `currency` when backend data is available.
- Persist invoices in backing store (Postgres/Firestore) to replace in-memory demo store.
- Correlate OTC mint submissions with on-chain confirmations to enrich dashboards.

