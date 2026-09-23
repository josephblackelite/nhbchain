# Fees and throttles runbook

This runbook covers the two fee mechanisms a node applies to native transfers, the
counters behind them, and how to check them. Everything below is what the code in
`native/fees`, `core/state_transition.go` and `core/state/fees.go` does.

## Domain fee policy: monthly free tier and MDR

**When it applies.** `applyTransactionFee` only runs for `TxTypeTransfer` (asset `NHB`)
and `TxTypeTransferZNHB` (asset `ZNHB`) transactions that carry a non-empty
`merchantAddr`, and only when that value names a domain that has a fee policy. The node
builds one policy from `[global.Fees]` and registers the same policy under a fixed list of three domain names
(`pos`, `p2p` and a third fixed name; `buildFeePolicyFromConfig` in `core/node.go`). A transfer without
`merchantAddr`, or with any other domain value, is not touched by this policy.

**Free tier.** The default is `100` transactions per payer, per domain, per UTC calendar
month (`DefaultFreeTierTxPerMonth` in `native/fees/apply.go`; `FreeTierTxPerMonth` in
`[global.Fees]`). A transaction is free when the payer's counter before it is below the
limit and the transferred amount is greater than zero. Transactions 1 to 100 in a month
are free (`freeTierApplied=true`); the 101st is charged.

**Counter scope.** Counters are shared across assets: NHB and ZNHB transfers draw from one
allowance (scope `__AGGREGATE__`). The `FreeTierPerAsset` flag exists in `fees.DomainPolicy`,
but `buildFeePolicyFromConfig` never sets it, so a node configured from `config.toml`
always uses the shared counter.

**Counter key.** `fees/counter/<domain>/<YYYYMM>/<scope>/<payer>`, where `<scope>` is
`__AGGREGATE__` and `<payer>` is the payer address as lowercase hex without a prefix
(`feeCounterKey` in `core/state/fees.go`). The stored value holds the count and the window
start (first day of the month, UTC).

**MDR.** Once the free tier is used up the fee is
`floor(amount * MDRBasisPoints / 10000)` in the transferred asset, capped at the amount.
The code default is `150` basis points (`DefaultMDRBasisPoints`); the sample `config.toml`
in the repository root sets `150` for NHB and `200` for ZNHB. Each `[[global.Fees.Assets]]`
entry sets `Asset`, `MDRBasisPoints` and `OwnerWallet`; the fee is debited from the fee payer
and credited to that asset's owner wallet (`applyTransactionFee`). The fee payer is
`DomainPolicy.FeePayer` (`sender` or `recipient`, `native/fees/apply.go`); `buildFeePolicyFromConfig`
never sets it, so on a node configured from `config.toml` it is the sender.

**Buyback split (NHB only).** When the state processor has a treasury buyback config and a
buyback accrual address set (`SetBuybackConfig`, `SetBuybackAccrualAddress`; `core/node.go`) and the fee asset is NHB, `FeeShareBps` of the fee is
credited to the buyback accrual account instead of the owner wallet, and the owner wallet
receives the remainder (`core/state_transition.go`, `applyTransactionFee`). `FeeShareBps` is read
from the governance parameter store when set there, otherwise from the genesis default
(`effectiveBuybackConfig`). ZNHB fees always go to the owner wallet in full. With no buyback
configuration the owner wallet receives the whole fee. No fee is charged while
the free tier applies. A transaction with an amount of zero still increments the payer's
counter but is neither free nor charged.

**Fees paid to the treasury wallet.** The sample `config.toml` points the
fee wallets at the network's admin/treasury wallet (the genesis `adminWallet`). Every ZNHB movement onto or off that wallet
that a transaction causes, including a ZNHB domain fee routed to it or a plain transfer to its
address, is booked into the ZNHB Reward Pool in the same state transition
(`treasuryZNHBFlowTracked` and `bookTreasuryPoolMovement`, `core/znhb_treasury_pool.go`). A
transaction that would move more ZNHB off the wallet than the Reward Pool holds is rejected with
`znhb: treasury reward pool cannot cover this outflow`. When the fee wallet is the sender or the
recipient of the ZNHB transfer itself, the fee is credited to that transfer's own account object
so the credit is not overwritten (`applyTransactionFee`).

**Where the values come from.** `FreeTierTxPerMonth`, `MDRBasisPoints` and the per-asset
entries are read from each node's `config.toml` (`[global.Fees]`) when the node starts. The
governance `PolicyDelta` in `native/gov/validate.go` covers only governance, slashing,
mempool and block limits, and the governance parameter allow-list in `config/config.go` has
no free-tier or MDR key, so no on-chain proposal changes them. Every validator computes
fees from its own loaded configuration.

## Transfer fee policy (separate mechanism)

`[global.Fees]` also configures a per-sender free spend allowance and a protocol fee on
plain transfers: `TransferFreeTierSpendWei` (default `1000000000000000000000`, which is
1000 NHB), `TransferFreeTierWindow` (`lifetime` or `monthly`, default `lifetime`),
`TransferFeeBps` (default `20`), `TransferFeeBpsZNHB` (default `10`) and
`TransferFeeCollector` (`config/config.go`, `core/transfer_gas_policy.go`). The policy is
disabled when the spend allowance is zero. Two read-only RPC methods report it
(`rpc/fees_handlers.go`):

* `fees_getTransferStatus` with params `[{"address": "<bech32>"}]` returns `window`,
  `window_key`, `spentWei`, `freeLimitWei`, `remainingWei`, `eligible` and, when set,
  `nextResetUnix`.
* `fees_getTransferQuote` with params
  `[{"address": "<bech32>", "asset": "NHB" or "ZNHB", "amountWei": "<positive integer>"}]`
  returns `eligible`, `feeWei` and `feeBps`.

## Checks

* **Aggregate status.** `nhb-cli fees status`, or the JSON-RPC method `fees_getMonthlyStatus`
  with empty params, returns `window_yyyymm`, `used`, `remaining` and
  `last_rollover_yyyymm`. `nhb-cli` posts to `--rpc <url>`, else `$RPC_URL`, else
  `http://localhost:8080` (`cmd/nhb-cli/main.go`; the sample `config.toml` serves RPC on
  `127.0.0.1:8545`, so pass `--rpc` or set `RPC_URL`).
* **Meaning of the numbers.** `used` counts free-tier transactions this month. The limit
  behind `remaining` grows by `FreeTierTxPerMonth` each time a payer's counter reaches 1
  for the month in any domain, so `remaining` is `limit - used` over the wallets seen so
  far, not a per-wallet number (`FeesRecordUsage` in `core/state/fees.go`).
* **Per transaction.** Each processed transaction of this kind emits a `fees.applied` event
  with `payer`, `domain`, `asset`, `grossWei`, `feeWei`, `netWei`, `policyVersion`,
  `ownerWallet`, `freeTierApplied`, `freeTierLimit`, `freeTierRemaining`, `usageCount`,
  `windowStartUnix` and `feeBps` (`core/events/fees.go`). `payer` and `ownerWallet` are hex.
* **Totals.** `fees_listTotals` with params `[{"domain": "pos"}]` returns one record per
  wallet with `domain`, `wallet`, `grossWei`, `feeWei` and `netWei` (`rpc/fees_query.go`).
* **Dashboard.** `ops/grafana/dashboards/fees.json` has the panels `Fee Total by Domain`,
  `Fee p95 per Transaction`, `Free Tier Burn Down` and `Route Balances`.

## Troubleshooting

1. **Unexpected charge.** Confirm the transaction carried `merchantAddr` for a configured
   domain, read its `fees.applied` event, and compare `usageCount` with `freeTierLimit`.
   The payer is charged when `usageCount` exceeds the limit.
2. **Wrong limit or rate.** Compare `[global.Fees]` in the `config.toml` of each validator.
   The values are not read from chain state.
3. **Allowance not reset.** The counter key includes the month, so a new month starts a new
   counter. See [fee operations](./fees-ops.md) for when the aggregate status rolls over.
