# Fee accounting

What the chain records and exposes about fees. Sources: `core/events/fees.go`,
`core/state/fees.go`, `core/state/transfer_gas.go`, `rpc/fees_handlers.go`,
`rpc/fees_query.go`.

## `fees.applied` event

Emitted by `applyTransactionFee` for every transfer that matches a fee domain
(see [fee policy](./policy.md#2-fee-domains-feesapply)), whether or not a fee
was charged. Attributes (`core/events/fees.go`); an attribute is omitted when
its value is empty or zero as noted:

| Attribute | Description |
| --- | --- |
| `payer` | Hex-encoded payer address (omitted if zero). |
| `domain` | Fee domain (`pos`, `p2p`, `otc`). |
| `asset` | `NHB` or `ZNHB`. |
| `grossWei` | Transferred value. |
| `feeWei` | Fee computed by `fees.Apply`. |
| `netWei` | `grossWei - feeWei` as computed by `fees.Apply`. The transfer itself credits the recipient the full `grossWei`, and the fee is debited from the payer separately. |
| `policyVersion` | Fee policy version (omitted when `0`); `buildFeePolicyFromConfig` sets it to `1`. |
| `ownerWallet` | Hex-encoded route wallet (omitted if zero). |
| `freeTierApplied` | `true` when the transfer was covered by the free tier. |
| `freeTierLimit` | `FreeTierTxPerMonth` for the domain. |
| `freeTierRemaining` | Free-tier transactions left in the month after this one. |
| `usageCount` | The payer's counter after this transaction (omitted when `0`). |
| `windowStartUnix` | Start of the UTC month applied (omitted when zero). |
| `feeBps` | Rate applied (omitted when `0`). |

A `feeWei` of `0` with `freeTierApplied=true` means the free tier covered the
transfer. The RPC explorer views render this event as `FeeApplied`.

## Transfer fee status

`fees_getTransferStatus` and `fees_getTransferQuote` report the protocol
transfer fee free tier (a per-wallet, per-asset spend ledger). Fields are listed
in [fee policy](./policy.md#1-protocol-transfer-fee-transfergaspolicy).
`fees_getTransferStatus` always reports the NHB ledger.

## Aggregated data

- `fees_getMonthlyStatus`: network-wide free-tier usage for the current UTC
  month (`window_yyyymm`, `used`, `remaining`, `last_rollover_yyyymm`). When a
  new month is first seen, the previous month's totals are stored as a snapshot
  at `fees/monthly/snapshot/<yyyymm>` (`FeesEnsureMonthlyRollover`); no RPC
  method returns those snapshots.
- `fees_listTotals` with `[{"domain": "<domain>"}]`: accumulated gross, fee and
  net per (domain, asset, route wallet), returned as
  `{domain, wallet, grossWei, feeWei, netWei}` records.

## Example

```bash
curl -s -X POST "$RPC_URL" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"fees_listTotals","params":[{"domain":"pos"}]}'
```

The `fees.applied` event stream and the totals RPC are the sources used by the
fee dashboard described in [the transparency page](../transparency/fees-dashboard.md).
For SQL and export examples see [`docs/queries/fees.sql`](../queries/fees.sql)
and [`docs/api/fees-query.md`](../api/fees-query.md).
