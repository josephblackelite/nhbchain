# Network-wide Fee Transparency Dashboard

The fees dashboard is the canonical reference for understanding how network fees accrue, are
recognized, and ultimately settle into the Network Hub Bank (NHB) treasury accounts. It combines
on-chain event data with custodied wallet balances to deliver a reconciled view of fee revenue and
free-tier consumption.

The authoritative Grafana configuration is versioned at
[`ops/grafana/dashboards/fees.json`](../../ops/grafana/dashboards/fees.json) and is validated by the
POS readiness test suite to ensure the JSON schema remains loadable.

## Methodology

The dashboard is composed of two data lenses that are reconciled on a daily basis:

1. **On-chain events** &mdash; Streaming the `FeeCharged` and `FeeSettled` events emitted by
   the settlement contracts. These events are used to calculate per-transaction fees, domain-level
   aggregates, and free-tier burns. All examples use the SQL and RPC snippets published in
   [`docs/queries/fees.sql`](../queries/fees.sql) and [`docs/api/fees-query.md`](../api/fees-query.md).
2. **Wallet balances** &mdash; Tracking the NHB owner multi-sig, ZNHB proceeds wallet, and USDC
   payout wallet across the supported L2 networks. Balance snapshots are ingested hourly and rolled
   up to daily closing positions.

The delta between event-derived fees and wallet balance movements is surfaced in the
**Reconciliation Checklist** panel so finance teams can investigate variances quickly.

## Core Panels

The dashboard publishes the following top-level widgets:

- **Daily Fee Totals** &mdash; Total fees collected in the last 24h with a trendline overlay for the
  last 14 days. This panel is backed by the `daily_fee_totals` query and highlights the split between
  protocol fees and passthrough network fees.
- **30-Day Moving Window** &mdash; A cumulative sum of daily fees over a rolling 30-day period to reveal
  gross revenue momentum.
- **Fee by Domain** &mdash; Bar chart comparing fee realization by domain (e.g. `gateway`, `swap`,
  `loyalty`). The Grafana panel uses the `fee_total_by_domain` series produced by the ClickHouse
  query in `docs/queries/fees.sql`.
- **Top Merchants** &mdash; Table of the top 20 merchants by fees charged over the selected time range,
  including transaction counts and average fee per transaction.
- **Free-tier Utilization** &mdash; Combines the `free_tier_burn_down` series and remaining allocation
  to highlight how much of the subsidized quota remains. Alerting is configured at 80% consumption.

## Drill-down Metrics

For deeper analysis, use the following interactive panels:

- **Fee p95 per Transaction** &mdash; P95, median, and minimum per-transaction fees to identify outlier
  routing costs.
- **Route Balances** &mdash; Wallet balance spark-lines for the NHB routing accounts (owner NHB wallet,
  ZNHB proceeds wallet, owner USDC wallet) with annotations for manual adjustments.
- **Free-tier Event Log** &mdash; Filterable table of free-tier grant issuances and burns for support
  teams investigating customer questions.

## Reconciliation Checklist

Finance should complete this checklist as part of daily close:

1. **Owner NHB wallet** &mdash; Confirm balance change matches the net of fees routed into NHB. Expected
   delta: `fee_total_by_domain` minus passthrough network reimbursements.
2. **ZNHB proceeds wallet** &mdash; Validate inflows align with fee settlements earmarked for ZNHB
   buybacks. Expected delta: 0 after accounting for pending swaps queued in the bridge contract.
3. **Owner USDC wallet** &mdash; Verify payouts to treasury match the USDC portion of collected fees.
   Expected delta: pending merchant reimbursements < 2% of prior-day fees.

Any discrepancies should be logged in the finance recon issue template and investigated within 24h.

## Operational Notes

- Dashboard auto-refresh interval is 5 minutes; p95 panels require 24h backfill after deployment.
- Queries are versioned alongside these docs. Update both the SQL and RPC references when modifying
  metrics to keep data consumers aligned.
