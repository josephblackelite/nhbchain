# Fee accounting telemetry

The fee router emits structured telemetry for every transaction that crosses
the policy boundary. These events power the POS readiness dashboards and the
ops reconciliation reports.

## Event stream

`fees.applied` is published for each assessed transaction with the following
attributes:

| Field | Description |
| --- | --- |
| `payer` | Hex-encoded address charged for the fee. |
| `domain` | Fee domain (e.g. `pos`). |
| `grossWei` | Settlement amount prior to fees. |
| `feeWei` | Amount routed to the treasury wallet. |
| `netWei` | Amount delivered to the merchant after fees. |
| `policyVersion` | Fee policy snapshot applied when the transaction executed. |
| `routeWallet` | Treasury wallet credited with the fee. |

Consumers should treat zero `feeWei` as an indicator that the free-tier covered
the transaction.

## Transfer gas threshold telemetry

Transfer gas sponsorship uses its own tracked spend ledger. Wallet and dashboard
surfaces can query `fees_getTransferStatus` to inspect:

| Field | Description |
| --- | --- |
| `window` | Sponsorship window mode (`lifetime` or `monthly`). |
| `window_key` | Active ledger bucket key. |
| `spentWei` | NHB already counted toward the free-transfer threshold. |
| `freeLimitWei` | Configured sponsored transfer spend limit. |
| `remainingWei` | Remaining sponsored headroom before gas is charged. |
| `eligible` | Whether the next NHB transfer is still gas-free. |
| `nextResetUnix` | Reset boundary for monthly sponsorship windows. |

## Aggregated totals

`fees_listTotals` exposes the on-chain ledger of fee accruals per domain and
wallet. Querying the `pos` domain returns totals keyed by the route wallet,
allowing dashboards to reconcile treasury balances with emitted events.

## Readiness check

The POS readiness test `TestSponsorshipCapsAndRouting` pushes transactions
through the free-tier boundary and validates that the owner wallet accrues the
expected balance delta. This ensures policy changes and router code paths are
covered by integration tests prior to release.
