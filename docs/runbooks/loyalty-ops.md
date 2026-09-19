# Loyalty Operations Runbook

This runbook covers the loyalty base reward engine in `native/loyalty` (`engine_base.go`)
and how the state processor invokes it (`core/state_transition.go`).

## What the base reward does

* **Trigger.** After a successful `TxTypeTransfer` (NHB) that has a recipient, the state
  processor calls the loyalty engine with token `NHB`, the sender as `From`, and the
  transaction value as the amount. The engine does nothing when the module is paused
  (see below). Any other token is skipped with reason `token_not_supported`. Transfers are
  not limited to merchant or commerce flows: the trigger is the NHB transfer itself.
* **Amount.** `reward = amount * baseBps / 10000`, paid in `ZNHB` from the configured
  treasury account to the spender (the sender, not the recipient). The reward is queued and
  settled at the end of the block (`QueuePendingBaseReward`), subject to the pro-rate
  settings in the global config's `dynamic` section.
* **Order of limits.** minimum spend, then per-transaction cap, then the sender's daily cap,
  then the sender/recipient pair's daily cap, then the treasury balance.

## Configuration

The global loyalty configuration is stored in chain state and is written from the
`loyaltyGlobal` object of the genesis file when the genesis is loaded
(`core/genesis/loader.go`, `core/genesis/spec.go`; the JSON key is `loyaltyGlobal`, see
`config/genesis.json`). The genesis reader rejects unknown top-level keys
(`DisallowUnknownFields` in `LoadGenesisSpec`), so a section named `loyalty` makes the genesis
fail to load instead of being applied. When `loyaltyGlobal` is absent, no global config is
written. Fields:

| Field | Meaning |
| --- | --- |
| `active` | The engine skips every transfer with reason `inactive` when false. |
| `treasury` | Account debited for rewards. |
| `baseBps` | Reward rate in basis points, at most `10000`. A value of `0` is replaced by the default `50` (`DefaultBaseRewardBps`, `native/loyalty/params.go`). The shipped genesis files in `config/` use `50`. |
| `minSpend` | Transfers below this amount are skipped with `below_min_spend`. |
| `capPerTx` | Maximum reward for one transfer. `0` disables the cap. |
| `dailyCapUser` | Maximum total base reward per sender per UTC day. `0` disables it. |
| `dailyCapCounterparty` | Maximum total reward per day for one sender/recipient pair, counted in either direction. `0` disables it. |
| `seedZNHB` | Amount of ZNHB (wei, decimal string) credited to the `treasury` account at genesis. When it is above zero the token `ZNHB` must be registered in `nativeTokens`, and the value is written into `alloc` for the `treasury` address, replacing any `ZNHB` amount already listed there for that same address string (`core/genesis/loader.go`). The shipped `config/genesis.json` uses `"0"`. |
| `dynamic` | Adaptive-rate and price-guard settings (`LoyaltyDynamicSpec`). |

In this repository no RPC method returns the global configuration, and no transaction,
proposal kind or RPC method rewrites it after genesis. The `loyalty.dynamic.*` governance
parameters are in the governance allow-list (`config/config.go`), but no code in this
repository reads them back into the global config. To read the current values, read the state
through a tool that opens the state trie (`Manager.LoyaltyGlobalConfig`).

## Pause and resume

The `loyalty` flag in the module pause map (`pauses.loyalty`, see [Pause and quota
operations](./pause-and-quotas.md)) makes `OnTransactionSuccess` return before any
reward logic runs, so no `loyalty.base.accrued` or `loyalty.base.skipped` event is emitted
while it is set (`native/loyalty/loyalty.go`). This is separate from pausing a single
merchant program with `TxTypePauseLoyaltyProgram`, which does not affect the base reward.

## Events

Both events carry `day` (UTC date of the block), `token`, `amount` (transfer amount),
`from` and `to` (hex addresses).

* `loyalty.base.accrued` adds `reward` (ZNHB, wei) and `baseBps`.
* `loyalty.base.skipped` adds `reason` and sometimes extra attributes. Reasons in the code:
  `config_error`, `inactive`, `missing_from_account`, `token_not_supported`,
  `invalid_addresses`, `self_transfer`, `amount_not_positive`, `below_min_spend`,
  `no_reward_rate`, `reward_zero`, `daily_cap_reached`, `counterparty_daily_cap_reached`,
  `meter_error`, `treasury_error` and `treasury_insufficient` (with `available`).

## Monitoring the treasury

Read the treasury's ZNHB balance with `nhb_getBalance` (params: the bech32 address; the
response includes `balanceZNHB`). Watch for `reason=treasury_insufficient` on
`loyalty.base.skipped`. The node also exports the gauges `nhb_loyalty_budget_zn`,
`nhb_loyalty_demand_zn`, `nhb_loyalty_prorate_ratio` and `nhb_loyalty_paid_today_zn`
(`observability/metrics.go`).

Note: the RPC method `nhb_getLoyaltyBudgetStatus` returns fixed placeholder values
(`rpc/explorer_handlers.go`), so do not use it for monitoring.

## Troubleshooting

1. **No rewards paid.** Look for `loyalty.base.skipped` events and read `reason`. If there
   are none, the loyalty module is paused or the transfer was not a successful NHB
   transfer with a recipient.
2. **Reward lower than expected.** `capPerTx`, `dailyCapUser` and `dailyCapCounterparty`
   all reduce the reward instead of skipping it, until a cap is fully used.
3. **Unexpected recipient.** The reward goes to the sender of the transfer
   (`Recipient` is set from `ctx.From` in `QueuePendingBaseReward`).
