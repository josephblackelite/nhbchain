# Fee operations runbook

This runbook supplements the policy reference with the concrete steps operators
follow when monitoring the free-tier allowance and month-end rollovers.

## Monthly rollover checklist

1. **Block one sanity check.** Verify that the first block after UTC midnight on
the first of the month executes `fees_getMonthlyStatus` (or `nhb-cli fees
status`) and reports the new `window_yyyymm`. The response should show
`last_rollover_yyyymm` equal to the prior month and zero `used`/`remaining`
counts.
2. **Snapshot verification.** Retrieve the snapshot via `nhb-cli fees status`
immediately after rollover and confirm that the previous month’s totals are
available in the audit store using the node’s state inspection tooling when
required for incident review.
3. **Legacy counters.** If a wallet reports non-zero usage in the new month,
confirm the free-tier counter increments from one. Any carry-over suggests a
legacy counter that must be purged via state tooling before transactions resume.

## Reconciliation

* **Usage vs. allowance.** During the month, track the `used` and `remaining`
fields from `fees_getMonthlyStatus`. `used` reflects the number of subsidised
transactions processed. `remaining` decreases as wallets draw down their
discretionary allowance.
* **Wallet sampling.** Pick a representative set of merchant and consumer
wallets. Compare their `fees.applied` events against the monthly aggregate to
confirm the counter logic is consistent.
* **Snapshot archival.** At the end of each month export the
`fees/monthly/snapshot/<YYYYMM>` records into the compliance archive. The
snapshot captures the total free-tier usage, aggregate allowance, and rollover
timestamp.

## Alerting

* **Rollover stall:** Alert if `last_rollover_yyyymm` lags the calendar month by
more than one day. The node should emit an operational incident in that case.
* **Utilisation surge:** Trigger a warning when `used` exceeds 80% of the
aggregate allowance (compute as `used + remaining`). Coordinate with governance
on whether an interim allowance increase is required.
* **Status endpoint health:** The observability stack polls
`fees_getMonthlyStatus`. Missing responses for more than two polling intervals
should raise an alert because downstream dashboards rely on the data.
