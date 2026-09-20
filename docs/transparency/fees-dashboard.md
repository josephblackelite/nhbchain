# Fee Transparency Dashboard

This page describes the Grafana dashboard definition and the on-chain fee data
it is meant to be fed from.

## What is in the repository

[`ops/grafana/dashboards/fees.json`](../../ops/grafana/dashboards/fees.json) is
a Grafana dashboard titled "NHB Fee Transparency" that refreshes every `5m`.
`tests/posreadiness/fees/reconcile_test.go` loads the file as part of the
readiness tests. Its four panels query ClickHouse tables:

| Panel | Type | Table it reads |
| --- | --- | --- |
| Fee Total by Domain | time series | `fee_total_by_domain` (last 30 days, grouped by `domain`) |
| Fee p95 per Transaction | time series | `fee_p95_per_tx` (95th percentile of `fee_per_tx`, last 30 days) |
| Free Tier Burn Down | bar gauge | `free_tier_burn_down` (`burned_ratio`; colour thresholds at `0.8` yellow and `0.9` red) |
| Route Balances | time series | `route_balances` (`wallet_label`, `balance_usd`, last 14 days) |

The ClickHouse tables are not created by any code in this repository. Loading
them is left to whoever runs the dashboard; example SQL and an export recipe
are in [`docs/queries/fees.sql`](../queries/fees.sql) and
[`docs/api/fees-query.md`](../api/fees-query.md).

## On-chain data available as inputs

- The `fees.applied` event (`TypeFeeApplied`, `core/events/fees.go`), emitted
  by `applyTransactionFee` in `core/state_transition.go`. Attributes are listed
  in [fee accounting](../fees/accounting.md). A node keeps at most the last
  100,000 such events, in memory (`core/event_log.go`), so a pipeline that needs
  the full history has to record them as blocks arrive.
- `fees_listTotals`, `fees_getMonthlyStatus`, `fees_getTransferStatus` and
  `fees_getTransferQuote` (`rpc/fees_handlers.go`, `rpc/fees_query.go`).
- Account balances of the route wallets, for example from `nhb_getBalance`.

`fees.applied` covers only transfers matched to a fee domain; the protocol
transfer fee (see [fee policy](../fees/policy.md#1-protocol-transfer-fee-transfergaspolicy))
does not emit it. Reconciling the two requires balance data for the transfer
fee collector as well.
