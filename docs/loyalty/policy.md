# Loyalty Daily Budget, Pro-rating and Dynamic Settings

This page covers the `dynamic` part of the loyalty global configuration: the daily budget that limits base rewards, the pro-rating step at the end of each block, and the settings that exist but are not (or only partly) used by the reward path. Code: `native/loyalty/global.go`, `native/loyalty/params.go`, `core/state/loyalty_budget.go`, `core/state/loyalty_engine.go`, `core/state_transition.go` (`EndBlockRewards`, `tickLoyaltySmoothing`), `observability/metrics.go`.

## Where the settings come from

* **At run time the engine reads the `GlobalConfig` record stored in chain state** (`Manager.LoyaltyGlobalConfig`), which is written once by the genesis loader from the genesis file's `loyaltyGlobal.dynamic` object (`core/genesis/spec.go`, `LoyaltyDynamicSpec`). No transaction or RPC updates it afterwards.
* The node's `config.toml` section `[global.Loyalty.Dynamic]` (`config/types.go`, `LoyaltyDynamic`) is loaded and validated, but it is not copied into the stored record. Its run-time uses in this repository are the production guard below (`Node.SetGlobalConfig`) and the preflight validation of governance proposals (`native/gov/validate.go`). Changing it does not change how rewards are paid.
* The loyalty parameter names `loyalty.dynamic.targetBps`, `minBps`, `maxBps`, `smoothingStepBps`, `coverageMax`, `coverageLookbackDays`, `dailyCapPctOf7dFees`, `dailyCapUsd`, `yearlyCapPctOfInitialSupply`, `priceGuard.pricePair`, `priceGuard.twapWindowSeconds`, `priceGuard.priceMaxAgeSeconds`, `priceGuard.maxDeviationBps` and `priceGuard.enabled` are accepted and validated by the governance engine (`native/governance/engine.go`). Nothing in the code applies an approved value to the stored `GlobalConfig`.

## Fields and defaults

Defaults when a genesis or config value is unset are applied by `ApplyDefaults` (`native/loyalty/params.go`); the TOML loader has its own defaults (`config/config.go`, `DefaultConfig`), shown in the last column.

| Field | Used by the reward path? | `ApplyDefaults` | TOML default |
|-------|--------------------------|-----------------|--------------|
| `targetBps` / `minBps` / `maxBps` / `smoothingStepBps` | Only by the smoothing tick (below); not by reward calculation | 50 / 25 / 100 / 5 | 50 / 25 / 100 / 5 |
| `coverageMax`, `coverageLookbackDays` | No | 5000 bps (0.50) / 7 | 0.50 / 7 |
| `dailyCapPctOf7dFees` | Yes, daily budget | 6000 bps (0.60) | 0.60 |
| `dailyCapUsd` | Yes, daily budget | 5000 | 5000 |
| `yearlyCapPctOfInitialSupply` | No | 1000 (10%) | 10 |
| `priceGuard.enabled` | Yes | not defaulted (false when omitted) | true |
| `priceGuard.pricePair` | Yes (base asset only) | `ZNHB/USD` | `ZNHB/USD` |
| `priceGuard.twapWindowSeconds` | No | 3600 | 7200 |
| `priceGuard.maxDeviationBps` | No | 500 | 300 |
| `priceGuard.priceMaxAgeSeconds` | Yes | 900 | 600 |
| `priceGuard.fallbackMinEmissionZNHBWei` | Yes | 0 | 0 |
| `priceGuard.useLastGoodPriceFallback` | Yes | not defaulted (false when omitted) | true |
| `enableProRate`, `enforceProRate` | Yes | true, true (when not explicitly set) | true, true |

The sample genesis files under `config/` set `priceGuard.enabled` true, `twapWindowSeconds` 7200, `maxDeviationBps` 300, `priceMaxAgeSeconds` 600 and `useLastGoodPriceFallback` true, with the other dynamic fields at the values in the table.

## Daily budget

`Manager.GetRemainingDailyBudgetZNHB` (`core/state/loyalty_engine.go`) returns `budget - paidToday` for the current UTC day (never below zero), where `budget` is computed by `CalcDailyBudgetZNHB`:

* If `dailyCapUsd > 0`: `dailyCapUsd / price`.
* If `dailyCapPctOf7dFees > 0`: `dailyCapPctOf7dFees * (rolling 7-day net ZNHB fees + rolling 7-day net NHB fees / price)`, from the fee tracker (`core/state/fees_rolling.go`).
* `budget` is the smaller of the values that apply; if neither applies it is 0.

`price` comes from `resolveLoyaltyPrice`:

* With `priceGuard.enabled` false, `price` is exactly 1.
* With it true, `price` is the last stored swap price proof for the base asset of `pricePair` (the part before `/`, default `ZNHB`). A proof older than `priceMaxAgeSeconds`, or no proof, counts as unavailable.
* When the price is unavailable: if `useLastGoodPriceFallback` is true and any earlier proof exists, that price is used and the fallback signal has strategy `last_good_price`; otherwise `budget` is 0.
* If `fallbackMinEmissionZNHB` is greater than zero and `budget` is 0 or lower than it, `budget` becomes that minimum (strategy `min_emission`, unless `last_good_price` already applied).

The remaining budget and the day's paid and proposed totals are readable over RPC with `nhb_getLoyaltyBudgetStatus` (see [`loyalty.md`](./loyalty.md) section 6); the values come from `Node.LoyaltyBudgetStatus`, which calls the same `GetRemainingDailyBudgetZNHB` the settlement uses.

A fallback signal increments the Prometheus counter `nhb_loyalty_price_fallback_total{strategy}` and emits `loyalty.price.fallback` with attributes `strategy`, `base`, `budget`.

`twapWindowSeconds` and `maxDeviationBps` are stored and validated but no code reads them, so there is no TWAP or deviation check.

## Pro-rating (base rewards only)

When the stored `dynamic.enableProRate` is true (the default, and the only value genesis can produce), each base reward is queued during block execution (`QueuePendingBaseReward`) and settled by `StateProcessor.EndBlockRewards` at the end of the block:

1. `demand` is the sum of the block's queued rewards; it is added to the day's "proposed" total.
2. `budget` is the remaining daily budget (above).
3. The ratio is `1` if `budget >= demand`, `budget / demand` if `0 < budget < demand`, and `0` if `budget <= 0`.
4. Each reward is paid as `reward * ratio` (integer division), never more than the remaining budget and never more than the treasury's balance, moved from the treasury account to the spender. The amount paid is added to the day's "paid" total. If the treasury is the node's admin/treasury wallet, the ZNHB Reward Pool ledger is adjusted by the net movement of the whole step in the same state transition (see [`payouts.md`](./payouts.md)).
5. If the ratio is below 1, `loyalty.budget.prorated` is emitted with `day` (`YYYY-MM-DD`), `budget_zn`, `demand_zn` and `ratio_fp` (ratio scaled by `1e18`).
6. Prometheus gauges are updated: `nhb_loyalty_budget_zn`, `nhb_loyalty_demand_zn`, `nhb_loyalty_prorate_ratio`, `nhb_loyalty_paid_today_zn` (the amounts are wei values converted to `float64`, not whole-ZNHB units; the ratio gauge is 0 to 1).

If `enableProRate` is false, `settleBaseRewardImmediate` pays each reward at once (capped only by the treasury balance), still records the proposed and paid totals, and emits `loyalty.budget.prorated` if the payout was smaller than the reward.

Business program rewards are not part of this queue and are not limited by the daily budget.

### Production guard

`Node.SetGlobalConfig` (`core/node.go`) fails when the network mode is `prod` (case-insensitive) and the loaded config has `EnforceProRate` true with `EnableProRate` false. The error text is `loyalty: pro-rate mode is enforced in production (loyalty.prorate.locked); set global.loyalty.Dynamic.EnforceProRate=false to override in non-production environments`.

## Smoothing tick

At every epoch boundary (`ProcessBlockLifecycle`, `core/epochs.go`) `tickLoyaltySmoothing` moves a stored "effective bps" value one `smoothingStepBps` step toward `targetBps` (clamped to `minBps`..`maxBps`) and emits `loyalty.smoothing.tick` with `effective_bps` and `target_bps`. The effective value is not read by `ApplyBaseReward`, which always uses the stored `baseBps`; the tick does not change reward amounts.

## Yearly emission cap (not enforced)

`LoyaltyEngineState.CanEmit` and the `loyalty.cap.hit` event type exist, but nothing in the reward paths calls `CanEmit`, nothing sets the state's `YearlyCapZNHB`, and `loyalty.cap.hit` is never emitted. `yearlyCapPctOfInitialSupply` therefore has no effect.

## Other loyalty events

* `loyalty.reward.proposed`: `tx_hash`, `amount` (emitted when a base reward is queued or settled immediately).
* `loyalty.budget.prorated`, `loyalty.price.fallback`, `loyalty.smoothing.tick`: as above.
