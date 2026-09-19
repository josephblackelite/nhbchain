# Paymaster Budget Runbook

This runbook covers the sponsorship limits the state processor enforces and where they are
configured. Sources: `core/sponsorship.go`, `config/global.go`, `config/types.go`,
`cmd/nhb/main.go`, `cmd/consensusd/main.go`, `core/tx/checks.go`.

## 1. What is checked, in order

When a transaction requests sponsorship (see [Paymaster administration](../launch/paymaster-admin.md)),
`EvaluateSponsorship` runs these checks after the module, signature and balance checks. The
first failure gives the status `throttled`. Only the cap results (steps 2 to 4) carry the
`throttle` details and produce a `paymaster.throttled` event; the POS registry results (step 1)
set only the status and reason, so the rejection is reported by `tx.sponsorship.failed` alone:

1. **POS registry.** The merchant and device named by the transaction's `merchantAddr` and
   `deviceId` are looked up in the [POS registry](./pos-onboarding.md). A paused merchant gives
   `merchant sponsorship paused`; a revoked device gives `device sponsorship revoked`; a device
   registered to a different merchant gives `device registered to merchant <address>`.
2. **Global daily cap.** The sum of `gasLimit * gasPrice` budgets already used today plus this
   one must not exceed `GlobalDailyCapWei`. (Steps 2 to 4 apply only when the budget is above
   zero, see below.)
3. **Merchant daily cap.** The same test per merchant against `MerchantDailyCapWei`. When this
   cap is set, a transaction without a `merchantAddr` is throttled with
   `merchant address required for sponsorship throttling`.
4. **Device transaction cap.** A device may have at most `DeviceDailyTxCap` sponsored
   transactions per day. When this cap is set, a transaction without both `merchantAddr` and
   `deviceId` is throttled with `device identifier required for sponsorship throttling`.

The three cap checks (steps 2 to 4, including both "required" checks) are skipped entirely when
the requested budget `gasLimit * gasPrice` is zero or less (`checkPaymasterCaps` in
`core/sponsorship.go` returns without a throttle). A sponsored transaction that signs off on a
zero `gasPrice` or a zero `gasLimit` is therefore never throttled by a cap, and a missing
`merchantAddr` or `deviceId` is not reported for it. The POS registry check in step 1 still runs.

A cap set to `0` (or an empty string) is not enforced. The day is the UTC calendar date of
the block time (format `2006-01-02`). The caps compare the requested budget, `gasLimit * gasPrice`,
not the amount finally charged.

## 2. Configuration

The limits are read from the `[global.Paymaster]` section of the node's `config.toml`:

```toml
[global.Paymaster]
  MerchantDailyCapWei = ""   # integer in wei, empty or 0 = no cap
  DeviceDailyTxCap = 0       # sponsored transactions per device per day, 0 = no cap
  GlobalDailyCapWei = ""     # integer in wei, empty or 0 = no cap
```

They are parsed and applied once, when `nhb` or `consensusd` starts (`SetPaymasterLimits` in
`cmd/nhb/main.go` and `cmd/consensusd/main.go`). A change takes effect after the node is
restarted. The values are per node configuration, not chain state. There are no metrics named
`pos_paymaster_*` in the code, and no RPC method returns the usage counters.

## 3. Monitoring

* **Events.** `paymaster.throttled` carries `scope` (global, merchant or device), `txHash`,
  `merchant`, `deviceId`, `day`, `limitWei`, `usedBudgetWei`, `attemptBudgetWei`, `txCount` and
  `limitTxCount`. `tx.sponsorship.applied` and `tx.sponsorship.failed` are listed in
  [Paymaster administration](../launch/paymaster-admin.md#5-events).
* **Preview.** `tx_previewSponsorship` shows `status`, `reason` and the `throttle` details for
  a transaction without submitting it.
* **State.** Per-day counters are stored per global scope, per merchant and per device
  (`core/state/paymaster_counters.go`): budget used, amount charged and transaction count.
  Read them with a tool that opens the state trie (`Manager.PaymasterCounters`).
* **Metrics.** The paymaster metrics the node registers are
  `nhb_paymaster_autotopups_total{outcome}` and `nhb_paymaster_autotopup_amount_wei_total{outcome}`
  (see [auto top-up](./paymaster-autotopup.md)). The POS lane metrics are in
  [POS SLA and troubleshooting](./pos-slas.md).

## 4. Automatic top-ups

The optional automatic top-up of a sponsor's ZNHB balance has its own runbook:
[Paymaster automatic top-up](./paymaster-autotopup.md).

## 5. Troubleshooting

* **New limits not applied.** The node was not restarted, or a different `config.toml` is
  in use. The values are not reloaded at runtime.
* **Unexpected `throttled` results.** Read the `reason` from `tx_previewSponsorship` and
  compare with the check list above. A paused merchant or a revoked device is the most common
  reason and does not involve a cap.
* **Repeated merchant cap exhaustion.** Raise `MerchantDailyCapWei` in the configuration of
  every validator and restart them; the caps are evaluated by each node's own configuration.
