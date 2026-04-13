# OTC Nightly Reconciliation Runbook

The OTC gateway now performs a nightly reconciliation that stitches together back-office invoices, submitted vouchers, and on-chain mint receipts exported via `swap_voucher_export`. The reconciler produces SLA dashboards, CSV/Parquet artifacts, and anomaly alerts to keep treasury and compliance in lock-step.【F:services/otc-gateway/recon/reconciler.go†L69-L492】【F:services/otc-gateway/swaprpc/client.go†L56-L194】

## Schedule and deployment

* **Scheduler cadence.** A background scheduler triggers the reconciler once per day at `OTC_RECON_RUN_HOUR`/`OTC_RECON_RUN_MINUTE` (default 01:05 local time) across a `OTC_RECON_WINDOW_HOURS` lookback (default 24h).【F:services/otc-gateway/config/config.go†L10-L125】【F:services/otc-gateway/recon/scheduler.go†L29-L83】
* **Runtime configuration.** Set `OTC_RECON_OUTPUT_DIR` for the report root, `OTC_RECON_DRY_RUN=true` to run in read-only/devnet mode, and adjust the timezone with `OTC_TZ_DEFAULT`. The OTC binary wires the scheduler when the service boots so no extra process management is required.【F:services/otc-gateway/config/config.go†L97-L125】【F:services/otc-gateway/main.go†L52-L92】
* **Manual execution.** Operators can invoke the reconciler directly (for ad-hoc windows or post-mortems) via the exported Go API or by running the unit tests that exercise real joins using sqlite fixtures: `go test -run TestReconcilerDryRunNoAnomalies ./services/otc-gateway/recon`.【F:services/otc-gateway/recon/reconciler_test.go†L37-L168】

## Data pipeline & joining strategy

1. **Invoice extraction.** All invoices touched in the window are loaded with receipts and decisions; branch metadata is pulled once per branch to recover caps and regions.【F:services/otc-gateway/recon/reconciler.go†L202-L239】  The reconciler also gathers event logs to recover minted timestamps.【F:services/otc-gateway/recon/reconciler.go†L242-L255】
2. **Voucher exports.** The swap RPC client pulls a CSV snapshot via `swap_voucher_export`, decodes the base64 payload, and normalises every column (provider identifiers, fiat amounts, oracle proofs, TWAP metrics, timestamps).【F:services/otc-gateway/swaprpc/client.go†L119-L194】  Provider transaction IDs drive the join between DB vouchers and on-chain mints.【F:services/otc-gateway/recon/reconciler.go†L272-L285】
3. **Metrics assembly.** For each invoice the reconciler calculates receipt, approval, and mint latencies; derives SLA compliance; and embeds branch policy limits alongside on-chain fiat/wei data.【F:services/otc-gateway/recon/reconciler.go†L289-L437】  The resulting `ReportRow` tracks both operational metrics and retention policy counters for downstream tooling.【F:services/otc-gateway/recon/reconciler.go†L90-L125】

### Reference SQL (optional cross-check)

```sql
-- Inspect a single branch/currency view for same-day exports
SELECT i.id,
       i.amount,
       i.currency,
       i.state,
       v.provider_tx_id,
       v.status,
       e.action,
       e.created_at
FROM   invoices i
LEFT JOIN vouchers v ON v.invoice_id = i.id
LEFT JOIN events e ON e.invoice_id = i.id AND e.action = 'invoice.minted'
WHERE  i.branch_id = :branch
  AND  i.created_at BETWEEN :start AND :end
ORDER BY i.created_at;
```

Use the SQL to confirm a local slice before comparing against the nightly CSV/Parquet outputs.

## Anomaly detection & alerting

The reconciler raises structured alerts (and logs via the injected `AlertFunc`) for each of the following conditions:

* **Missing mints** – Invoice or voucher marked minted but no matching on-chain export row.【F:services/otc-gateway/recon/reconciler.go†L336-L400】
* **Amount mismatches** – Fiat totals diverge by more than $0.01 between accounting and export.【F:services/otc-gateway/recon/reconciler.go†L365-L387】
* **Expired vouchers** – Voucher TTL elapsed without a mint confirmation.【F:services/otc-gateway/recon/reconciler.go†L344-L356】
* **Branch cap breaches** – Daily totals exceed branch region caps; every impacted row is flagged and a one-off alert is emitted.【F:services/otc-gateway/recon/reconciler.go†L440-L460】

Alerts surface in the reconciliation result and can be routed into ticketing/Slack by supplying a custom `AlertFunc`.【F:services/otc-gateway/recon/reconciler.go†L494-L500】

## SLA metrics & reports

* **Latency tracking.** Receipt, approval, and mint latencies plus a 24h SLA flag are persisted for every invoice.【F:services/otc-gateway/recon/reconciler.go†L327-L334】【F:services/otc-gateway/recon/reconciler.go†L402-L435】
* **CSV exports.** Each branch/currency combination yields a CSV containing audit-ready columns (identifiers, statuses, latencies, retention counters, caps).【F:services/otc-gateway/recon/reconciler.go†L512-L595】
* **Parquet exports.** The same rows are emitted as Parquet with typed schema for downstream analytics warehouses.【F:services/otc-gateway/recon/reconciler.go†L597-L692】
* **File layout.** Reports are stored in `<OTC_RECON_OUTPUT_DIR>/<start>_<end>/<branch>_<currency>.{csv,parquet}` with completion logs for traceability.【F:services/otc-gateway/recon/reconciler.go†L462-L533】  Dry-runs skip file emission but still return the assembled rows for inspection.【F:services/otc-gateway/recon/reconciler.go†L200-L487】

## Dry-run and acceptance testing

* **Devnet smoke test.** `OTC_RECON_DRY_RUN=true` ensures the service collects metrics without touching disk—ideal for devnet or pre-production verification.【F:services/otc-gateway/config/config.go†L97-L125】【F:services/otc-gateway/recon/reconciler.go†L200-L487】
* **Mismatch injection.** The unit test `TestReconcilerDetectsAmountMismatch` feeds a synthetic under-minted export and asserts alert delivery, proving the pipeline raises mismatches as required.【F:services/otc-gateway/recon/reconciler_test.go†L109-L168】

## Retention guidance

Retention thresholds are embedded in every report row and should inform downstream storage policies: receipts (365 days), compliance decisions (730 days), and generated reports (18 months).【F:services/otc-gateway/recon/reconciler.go†L27-L125】【F:services/otc-gateway/recon/reconciler.go†L487-L490】  When archiving, ensure exported CSV/Parquet and associated alerts remain available for the same periods.

