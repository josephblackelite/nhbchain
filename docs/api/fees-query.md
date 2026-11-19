# Fees query API

The node exposes on-chain fee data through five JSON-RPC methods
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
`freeLimitWei`, `remainingWei`, `eligible`, `nextResetUnix` when known, and
`strictAllowance` (per-address transfer free-tier state). `strictAllowance` is `true` when the next
block charges the strict allowance: only the part of a transfer above `remainingWei` pays the fee
(see [the strict free allowance](../governance/fee-params.md#the-strict-free-allowance-transferfeestrictallowanceactivationheight));
`false` means a transfer is free in full while `remainingWei` is positive.

## `fees_getTransferQuote`

Params: `[{"address": "nhb1...", "asset": "NHB" | "ZNHB", "amountWei": "..."}]`.
`amountWei` must be a positive decimal integer. Result: `eligible` (true when the allowance
covers the whole transfer and the fee is `"0"`), `feeWei`, `feeBps` (the rate for that asset),
`freeWei` (the part of the amount the allowance covers), `chargeableWei` (the rest, the part the
fee is charged on) and `strictAllowance` (see `fees_getTransferStatus`). Without the strict
allowance a transfer is free in full while any allowance is left, so `freeWei` is the whole amount
and `chargeableWei` is `"0"`. Once a governed fee schedule is in force (see
[fee policy configuration](../governance/fee-params.md#governing-the-transfer-fee-schedule-policyfeeparams))
the quote and `fees_getTransferStatus` use its rate and allowance, not the node's file.

## `fees_getTransferSchedule`

Params: none. An open read that changes nothing. Result: `nextBlockHeight`; `inForce` (the schedule the
next block will charge: `nhbFeeBps`, `znhbFeeBps`, `freeAllowanceWei`, `source` = `"config"` or
`"governance"`, and for a governed one `proposalId` and `fromHeight`); `pending` (the same shape, or
`null`: a schedule that passed governance and is waiting for its block) with `blocksUntilPending`;
`uniformDomainFee` (`active`, `activationHeight`); `strictAllowance` (`active`, `activationHeight`:
whether the next block charges the fee only on the part of a transfer above what is left of the free
allowance); `governance` (`active`, `activationHeight`,
`delayBlocks`, `maxFeeBps`, `maxFreeAllowanceWei`).

## `fees.applied` event

Emitted by `applyTransactionFee` (`core/state_transition.go`) for NHB
(`TxTypeTransfer`) and ZNHB (`TxTypeTransferZNHB`) transfers whose `merchantAddr`
field names a domain that has a configured fee policy; other transfers emit no
`fees.applied` event. From `UniformTransferFeeActivationHeight` the event reports the
transfer fee that was routed to the domain's wallet (`feeWei` is `0` when the transfer was inside the
free allowance, a sponsor paid the fee, or the domain has no route wallet), `feeBps` is the ordinary
rate, and `freeTierLimit`, `freeTierRemaining` and `usageCount` are `0` (the per-domain free tier is not
used). Attributes (all strings; keys are omitted when empty or zero
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
