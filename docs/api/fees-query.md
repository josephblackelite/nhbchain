# Fees query API

The node exposes on-chain fee data through four JSON-RPC methods
(`rpc/fees_handlers.go`, `rpc/fees_query.go`) and the `fees.applied` event
(`core/events/fees.go`). None of these methods requires auth. See
[rpc.md](./rpc.md) for transport rules (`id` must be an integer; `params` is an
array).

## `fees_listTotals`

Params: `[{"domain": "<domain>"}]`. `domain` is required.

```bash
curl -s -X POST "$RPC_URL" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"fees_listTotals","params":[{"domain":"'"$DOMAIN"'"}]}' \
  | jq '.result'
```

Result: an array with one entry per wallet: `domain`, `wallet` (bech32),
`grossWei`, `feeWei`, `netWei` (decimal strings).

## `fees_getMonthlyStatus`

Params: none. Result: `window_yyyymm`, `used`, `remaining`,
`last_rollover_yyyymm` (network-wide free-tier usage for the current month).

## `fees_getTransferStatus`

Params: `[{"address": "nhb1..."}]`. Result: `window`, `window_key`, `spentWei`,
`freeLimitWei`, `remainingWei`, `eligible`, and `nextResetUnix` when known
(per-address transfer free-tier state).

## `fees_getTransferQuote`

Params: `[{"address": "nhb1...", "asset": "NHB" | "ZNHB", "amountWei": "..."}]`.
`amountWei` must be a positive decimal integer. Result: `eligible`, `feeWei`
(`"0"` when the address is within its free tier), `feeBps` (the rate for that
asset).

## `fees.applied` event

Emitted by `applyTransactionFee` (`core/state_transition.go`) for NHB
(`TxTypeTransfer`) and ZNHB (`TxTypeTransferZNHB`) transfers whose `merchantAddr`
field names a domain that has a configured fee policy; other transfers emit no
`fees.applied` event. Attributes (all strings; keys are omitted when empty or zero
as noted):

| Attribute | Description |
| --- | --- |
| `payer` | Hex-encoded 20-byte payer address (no `0x` prefix); omitted if zero. |
| `domain` | Fee domain. |
| `asset` | Asset ticker, upper-cased. |
| `grossWei`, `feeWei`, `netWei` | Decimal amounts. |
| `policyVersion` | Policy version (omitted if zero). |
| `ownerWallet` | Hex-encoded 20-byte wallet that accrued the fee (no `0x` prefix); omitted if zero. |
| `freeTierApplied` | `true` when the free tier was consumed instead of paying a fee. Always present. |
| `freeTierLimit` | Free-tier allowance in transactions. Always present. |
| `freeTierRemaining` | Free-tier transactions remaining. Always present. |
| `usageCount` | Post-increment usage counter (omitted if zero). |
| `windowStartUnix` | Unix seconds at the start of the billing window (omitted if unset). |
| `feeBps` | Effective fee basis points when a fee was charged (omitted if zero). |

In `nhb_getTransactionReceipt` logs the `payer` and `ownerWallet` values get a
`0x` prefix and `grossWei`/`feeWei`/`netWei` are returned as hex under
`gross`/`fee`/`net`.

## Loading events into an analytics store

The node has no export command that writes a fee-events file, and the event does
not carry the columns `tx_id`, `block_timestamp`, `merchant_id`, `merchant_name`
or the USD/USDC conversions. If you index `fees.applied` events yourself, you
must derive those columns from the transaction and block that contained the
event. The sample queries in [`docs/queries/fees.sql`](../queries/fees.sql)
assume a `fee_events` table with columns `tx_id`, `block_timestamp`, `domain`,
`merchant_id`, `merchant_name`, `fee_amount_native`, `fee_amount_usdc`,
`fee_amount_usd`; that schema is defined by those queries, not by the node.
