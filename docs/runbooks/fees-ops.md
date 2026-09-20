# Fee operations runbook

Operational checks for the aggregate monthly free-tier status. The counters and rules are
described in [fees and throttles](./fees-and-throttles.md).

## How the monthly status rolls over

The aggregate status is one record at the state key `fees/monthly/status`
(`core/state/fees.go`). It holds the window (`YYYYMM`), `used`, the accumulated limit, the
number of wallets and `last_rollover_yyyymm`.

The rollover is lazy. It happens inside the first fee-policy transaction processed in a new
UTC month (`FeesRecordUsage` calls `FeesEnsureMonthlyRollover`). At that moment the node:

1. writes the previous month's totals to `fees/monthly/snapshot/<YYYYMM>` (`used`, `limit`,
   `wallets` and the completion time),
2. sets `last_rollover_yyyymm` to the previous window,
3. resets `used`, the limit and the wallet count to zero.

`fees_getMonthlyStatus` only reads the stored record. If no fee-policy transaction has been
processed yet in the new month it still returns the previous month's window, and
`last_rollover_yyyymm` is unchanged. Before any usage has ever been recorded,
`nhb-cli fees status` prints `No monthly usage has been recorded yet.`

## Monthly checklist

1. After the first fee-policy transaction of the month, run `nhb-cli fees status`
   (`--rpc <url>` selects the node). `Window` must be the new month, `Last rollover` the
   previous one, and `Used` a small number.
2. To review the previous month, read the state key `fees/monthly/snapshot/<YYYYMM>`. No
   RPC method returns snapshots; `FeesMonthlySnapshot` in `core/state/fees.go` is the
   accessor.
3. Per-wallet counters are stored per month (`fees/counter/<domain>/<YYYYMM>/...`), so a
   wallet that shows old usage after the rollover points at the wrong window or domain in
   the counter key. A legacy key without month or scope (`fees/counter/<domain>/<payer>`)
   is still read for the shared scope, but only when its stored window falls in the
   current month (`FeesGetCounter`).

## Reconciliation

* Compare the `fees.applied` events (`freeTierApplied`, `usageCount`) for sampled wallets
  with the aggregate `used` value.
* Per-domain, per-wallet fee totals come from the JSON-RPC method `fees_listTotals` with
  params `[{"domain": "pos"}]` (`rpc/fees_query.go`) and from the dashboard
  `ops/grafana/dashboards/fees.json`.

## Alerting

The repository ships no alert rules for the fee status. Signals you can derive from the RPC
response are:

* `last_rollover_yyyymm` lagging the current month while fee-policy transactions are being
  processed.
* `used` approaching `used + remaining`.
* `fees_getMonthlyStatus` failing to respond. On errors it returns HTTP 503
  `node unavailable` or HTTP 500 `failed to load monthly status` (`rpc/fees_handlers.go`).
