# Paymaster Sponsorship Guardrails

When a transaction names a paymaster (gas sponsor), the state processor checks
the sponsorship against daily caps before accepting it (`core/sponsorship.go`).
The caps are read from `[global.Paymaster]` when the node starts, by both
`cmd/nhb` (the binary `nhb.service` runs) and `cmd/consensusd`
(`cmd/nhb/main.go` and `cmd/consensusd/main.go`, `Node.SetPaymasterLimits`).

## Configuration

| Key | Type | Meaning |
| --- | ---- | ------- |
| `MerchantDailyCapWei` | string | Maximum sponsored gas cost (wei) per merchant per day. `0` disables the check. |
| `DeviceDailyTxCap` | integer | Maximum number of sponsored transactions per merchant device per day. `0` disables the check. |
| `GlobalDailyCapWei` | string | Maximum sponsored gas cost (wei) across all merchants per day. `0` disables the check. |

All three default to `0` (`defaultGlobalConfig`). The amount strings are parsed
by `config.ParseAmount`: digits with optional `_` separators, an optional decimal
point and an optional `e` exponent, as long as the result is a non-negative
integer (for example `250000000000000000000` or `250e18`). An invalid value makes
either binary panic at startup with `Failed to parse paymaster limits`. Restart the node to apply changes.

```toml
[global.Paymaster]
MerchantDailyCapWei = "250e18"
DeviceDailyTxCap = 200
GlobalDailyCapWei = "1000e18"
```

`[global.Paymaster.AutoTopUp]` configures automatic replenishment (`Enabled`,
`Token`, `MinBalanceWei`, `TopUpAmountWei`, `DailyCapWei`, `CooldownSeconds`, and
`[global.Paymaster.AutoTopUp.Governance]` with `FundingAccount`, `Minter`,
`Approver`, `MinterRole`, `ApproverRole`). `Token` is `NHB` (the default, and
what an empty value means) or `ZNHB`, and `DailyCapWei` must be positive when
`Enabled` is true (`config/global.go`, `PaymasterAutoTopUpConfig`). A top-up uses
one asset, the policy's `Token`: the balance check, the funding-account debit,
the paymaster credit and the flat fee all use that asset
(`core/sponsorship.go`, `maybeAutoTopUpPaymaster`). A top-up does not run, and
records a failure event with reason `funding_account_in_use` or
`treasury_account_in_use`, when the funding account (or, if a fee is due, the
fee treasury) is one of the accounts the same transfer is already updating.
See the [auto top-up runbook](../runbooks/paymaster-autotopup.md).

## How the caps are enforced

For each sponsored transaction the state processor computes the gas cost
(`checkPaymasterCaps`, `core/sponsorship.go`) and checks, in this order:

1. Global: used budget for the day plus this cost must not exceed
   `GlobalDailyCapWei`.
2. Merchant (when `MerchantDailyCapWei > 0`): the transaction must carry a
   merchant address, and the merchant's used budget plus this cost must not
   exceed the cap.
3. Device (when `DeviceDailyTxCap > 0`): the transaction must carry both a
   merchant address and a device ID, and the device's transaction count for the
   day must be below the cap.

The day is the UTC date (`2006-01-02`) of the block timestamp, so counters start
again at UTC midnight. A failed check marks the sponsorship as throttled with the
reason `global sponsorship cap reached`, `merchant sponsorship cap reached`,
`device sponsorship cap reached`, or a message saying the merchant or device
identifier is required.

## Monitoring

- The `paymaster.throttled` event is emitted for each throttled attempt
  (`core/events/sponsorship.go`). Attributes: `scope` (`merchant`, `device` or
  `global`), `txHash`, `merchant`, `deviceId`, `day`, `limitWei`, `usedBudgetWei`,
  `attemptBudgetWei`, `txCount` and `limitTxCount` (unset ones are omitted).
- `tx_previewSponsorship` returns the assessment for a transaction payload
  without executing it: `status`, `reason`, `sponsor`, `gasPriceWei`,
  `requiredBudgetWei`, `moduleEnabled` and a `throttle` object with the same
  fields as the event. `tx_getSponsorshipConfig` returns `enabled` and
  `adminRole`. `tx_setSponsorshipEnabled` is disabled: it answers HTTP 410 with
  code `-32060`, and sponsorship stays enabled on every node
  (`handleTxSetSponsorshipEnabled` in `rpc/http.go`).
- Auto top-ups are counted by `nhb_paymaster_autotopups_total{outcome}` and
  `nhb_paymaster_autotopup_amount_wei_total{outcome}`.

`TransactionsModule.SponsorshipCounters` in `rpc/modules/transactions.go` can
return per-day counters (`budgetWei`, `chargedWei`, `txCount` for merchant,
device and global scope), but `rpc/http.go` has no method routed to it, so no
RPC exposes the counters today.

## Troubleshooting a throttled merchant

1. Find the latest `paymaster.throttled` events for the merchant and read `scope`,
   `limitWei` / `limitTxCount` and `usedBudgetWei` / `txCount`.
2. Raise the relevant cap in `[global.Paymaster]` and restart the nodes, or
   investigate the traffic.
3. Counters are keyed by UTC day, so a new day starts from zero.
