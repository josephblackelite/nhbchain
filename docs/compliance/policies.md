# OTC Reconciliation Compliance Policy

This policy describes how nightly reconciliation artifacts satisfy regional reporting, retention, and audit controls for OTC mints.

## Record retention

* **Receipts:** Retain uploaded receipt objects and their metadata for a minimum of 365 days to satisfy merchant evidence obligations.【F:services/otc-gateway/recon/reconciler.go†L27-L431】
* **Compliance decisions:** Preserve supervisor/compliance decisions, including notes and timestamps, for at least 730 days to support regulator look-backs and conduct reviews.【F:services/otc-gateway/recon/reconciler.go†L27-L433】
* **Generated reports:** Store nightly CSV/Parquet reports and the anomaly trail for 18 months (545 days). This ensures regulators can replay any period and verify that alerts were triaged.【F:services/otc-gateway/recon/reconciler.go†L27-L535】【F:services/otc-gateway/recon/reconciler.go†L487-L490】
* **Storage location:** Reports are materialised beneath `OTC_RECON_OUTPUT_DIR` in date-stamped folders. Archive the folder hierarchy intact so that future audits can cross-reference the creation logs and branch-specific summaries.【F:services/otc-gateway/recon/reconciler.go†L462-L533】【F:services/otc-gateway/config/config.go†L97-L125】

## Regional transparency

Every `ReportRow` includes branch name, region, currency, and cap metadata so regulators can verify local limits without recomputing ledger state.【F:services/otc-gateway/recon/reconciler.go†L90-L437】  Branch-specific totals are compared against configured region caps; any breach is flagged and alerted once per region to provide a tamper-evident escalation trail.【F:services/otc-gateway/recon/reconciler.go†L440-L460】  Store regional reports alongside country-specific compliance workpapers for rapid response during examinations.

## On-chain verification package

The nightly reports bundle:

1. **Accounting view** – invoice state, amounts, receipt counts, and decision counts.
2. **On-chain view** – export status, oracle references, mint amounts, price proofs, and TWAP metadata from `swap_voucher_export`.
3. **SLA metrics** – receipt/approval/mint latencies and the `SLAWithin24h` flag for service continuity attestations.
4. **Anomalies** – structured descriptors for missing mints, amount mismatches, expirations, and cap breaches with linked invoice/provider IDs.【F:services/otc-gateway/recon/reconciler.go†L336-L460】

Presenting all four components allows regulators to trace any mint from intake through on-chain settlement while observing the real-time controls that guarded the transaction.

## Operational workflow

* **Scheduler oversight:** Operations should monitor that the scheduler runs at the configured cadence (default 01:05 local time). Missed runs must be rerun manually with the same window to avoid retention gaps.【F:services/otc-gateway/recon/scheduler.go†L29-L83】
* **Dry-run guardrails:** In sandboxes or devnet, enable `OTC_RECON_DRY_RUN` so the service exercises the pipeline without publishing artifacts. Before production rollout, disable the flag and validate a live run, confirming the generated files land in the compliance archive.【F:services/otc-gateway/config/config.go†L97-L125】【F:services/otc-gateway/main.go†L66-L92】
* **Alert response:** Any anomaly requires a documented follow-up (e.g., treasury mint replay, voucher renewal, or limits review). Append the resolution to the compliance case using the anomaly `Type`, `InvoiceID`, and `ProviderTxID` embedded in the report for traceability.【F:services/otc-gateway/recon/reconciler.go†L81-L500】

## Evidence for audits

When responding to regulator requests provide:

* The relevant nightly CSV and Parquet files, along with the run directory log snippet showing when the scheduler produced the artifacts.【F:services/otc-gateway/recon/reconciler.go†L462-L533】
* The anomaly list and remediation tickets referencing each `Type` and invoice identifier.【F:services/otc-gateway/recon/reconciler.go†L336-L460】
* Proof that retention windows were honoured (e.g., storage bucket lifecycle policy or storage audit results) referencing the configured day counts above.

Following this policy keeps OTC operations demonstrably compliant while giving auditors a deterministic replay of the mint workflow.

