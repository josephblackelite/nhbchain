# Loyalty Dynamic Policy Defaults

The loyalty engine ships with an adaptive controller that gently adjusts the base reward rate over time. The following table summarises each dynamic field and the default values bundled with the node binary.

| Field | Description | Default |
| --- | --- | --- |
| `TargetBps` | Desired long-run basis-point rate the controller attempts to converge to when activity stabilises. | `50` bps |
| `MinBps` | Lower bound of the permitted basis-point band for automatic adjustments. | `25` bps |
| `MaxBps` | Upper bound of the permitted basis-point band for automatic adjustments. | `100` bps |
| `SmoothingStepBps` | Maximum change (in basis points) applied per adjustment cycle; smaller values produce gradual moves. | `5` bps |
| `CoverageMax` | Maximum coverage ratio considered healthy before rewards are dampened. | `0.50` (50%) |
| `CoverageLookbackDays` | Rolling window (in UTC days) of settlement activity observed before recomputing the dynamic rate. | `7` days |
| `DailyCapPctOf7dFees` | Maximum share of the trailing seven-day fee pool that can be emitted each day. | `0.60` (60%) |
| `DailyCapUSD` | Network-wide soft cap on ZNHB minted through dynamic boosts each day, expressed in whole USD. | `5,000` USD |
| `YearlyCapPctOfInitialSupply` | Network-wide soft cap on annual dynamic issuance relative to the initial ZNHB supply. | `10` % |
| `PriceGuard.Enabled` | Toggles price sanity checks when consuming oracle data to estimate coverage ratios. | `true` |
| `PriceGuard.PricePair` | Oracle trading pair queried when evaluating coverage. | `ZNHB/USD` |
| `PriceGuard.TwapWindowSeconds` | TWAP smoothing window applied to the oracle pair before computing deviations. | `7,200` seconds |
| `PriceGuard.MaxDeviationBps` | Maximum tolerated oracle variance relative to the rolling average before adjustments are frozen. | `300` bps |
| `PriceGuard.PriceMaxAgeSeconds` | Maximum age of oracle data before the controller halts adjustments. | `600` seconds |
| `EnableProRate` | Toggles queue-and-settle behaviour for base rewards; disabling settles immediately. | `true` |
| `EnforceProRate` | Prevents disabling pro-rate mode in production environments unless explicitly overridden. | `true` |

Operators can override these settings in `config.toml` under the `[global.loyalty.Dynamic]` section. Leave any field unset (or zero) to continue using the compiled defaults above.

## Yearly emission cap

The yearly cap limits how much ZNHB the loyalty engine may emit across base and program rewards within a calendar year. The cap is derived from the configured `YearlyCapPctOfInitialSupply` percentage and the initial ZNHB supply. Each time a reward is applied the engine increments the year-to-date counter. When an emission would exceed the yearly cap the payout is rejected, the counter remains unchanged, and a `loyalty.cap.hit` event is emitted with the attempted amount, configured cap, cumulative emissions, and the remaining headroom (`0` once saturated). Exact matches to the cap are permitted and flagged with the same event so operators can prepare replenishment or governance actions.

## Pro-rate mode

When `Dynamic.EnableProRate` is enabled (the default is `true`) the loyalty engine queues all computed base rewards as pending throughout block execution. Each call to `QueuePendingBaseReward` records the intended recipient, amount, and originating transaction hash without immediately moving funds.

The pending queue is settled during `EndBlockRewards`. The engine totals the queued demand for the current UTC day and compares it to the remaining daily budget resolved from the rolling fee tracker. If the budget is sufficient, every reward pays out in full. When demand exceeds the available budget, the engine applies a uniform proration ratio to every payout, deducts that amount from the treasury account, and persists the partial payments. The prorate ratio and the running daily counters are also exported to telemetry so operators can see when the safety rails are engaged.

Whenever a partial payout occurs the block emits a `LoyaltyBudgetProRated` event with the following fields:

| Attribute | Description |
| --- | --- |
| `day` | UTC date (`YYYY-MM-DD`) of the settlement window. |
| `budget_zn` | Remaining ZNHB budget before settlement. |
| `demand_zn` | Total queued ZNHB demand for the day. |
| `ratio_fp` | Fixed-point ratio (`1e18` scale) applied to rewards. |

Set `Global.Loyalty.Dynamic.EnableProRate = false` in `config.toml` to disable the queue-and-settle flow. In that mode rewards settle immediately and skip the pro-rate safeguards entirely.

### Production enforcement guard

Production deployments (`NHB_ENV=prod`) refuse to start when `EnableProRate = false` while `EnforceProRate = true`. The daemon exits with a fatal error containing `loyalty.prorate.locked` and guidance to set `global.loyalty.Dynamic.EnforceProRate=false` before retrying. Operators can only make that override in non-production environments; leaving enforcement enabled ensures mainnet runs always settle through the prorating queue.

When the guard fires, operations dashboards will show the fatal startup message and the service health check will remain `DOWN`. Clearing the condition (either by re-enabling pro-rating or by disabling the enforcement flag outside production) allows the node to boot normally, after which the standard pro-rate telemetry (`loyalty_prorate_ratio`, `LoyaltyBudgetProRated` events) resumes.

### End-of-block pro-rate

`EndBlockRewards` resolves the day's outstanding demand, computes the pro-rate ratio, and emits the settlement telemetry used by the Loyalty Budget dashboard:

1. Aggregate all pending base rewards for the current UTC day (`loyalty_demand_zn`).
2. Compare demand with the remaining budget (`loyalty_budget_zn`).
3. Clamp the payout multiplier to `min(1, budget / demand)` and expose it as `loyalty_prorate_ratio`.
4. Apply the multiplier to each queued reward and increment `loyalty_paid_today_zn` with the distributed amount.

This process ensures every participant within the settlement window receives the same proportional treatment. Operators can watch `loyalty_prorate_ratio < 1` alongside the `LoyaltyBudgetProRated` events to confirm the throttling was intentional rather than the result of oracle guard rails.

#### Before/after example

- **Before `EndBlockRewards`**: The queue holds 1,250 ZNHB in pending rewards while the budget has 1,000 ZNHB remaining. No payouts have been applied yet, so `loyalty_prorate_ratio` is unset.
- **After `EndBlockRewards`**: The engine computes a ratio of `0.80`, emits `LoyaltyBudgetProRated{day="2024-03-05", budget_zn="1000", demand_zn="1250", ratio_fp="0.8e18"}`, and each recipient sees their reward scaled to 80% of the original amount. Metrics reflect `loyalty_prorate_ratio = 0.80`, `loyalty_budget_zn = 0`, and `loyalty_paid_today_zn = 1,000`.

### Daily budget & events (`LoyaltyBudgetProRated`)

The daily cap is derived from the trailing seven-day fee pool (`DailyCapPctOf7dFees`) and any explicit hard limit (`DailyCapUSD`). When the cap is recalculated at the UTC boundary the controller resets `loyalty_budget_zn` and clears the pending queue. Throughout the day each block settlement updates:

- `loyalty_budget_zn` – how much headroom is left for the day.
- `loyalty_demand_zn` – the queued demand observed at the start of `EndBlockRewards`.
- `loyalty_prorate_ratio` – the multiplier applied to all payouts.
- `LoyaltyBudgetProRated` – the event stream operators follow in Grafana/LogQL and in the alert pipeline.

The observability stack also fans these events into `loyalty_budget_events_total` to count how many blocks triggered proration, which backs the "Prorate Hits" stat on the Loyalty Budget dashboard. A sudden spike in the counter signals that fee inflows or coverage assumptions have shifted and the policy may require governance intervention.

### Price guards (TWAP window, deviation) with `MinBps` fallback

When `PriceGuard.Enabled = true` the policy protects against stale or manipulated oracle inputs before recomputing coverage ratios:

1. Fetch the latest oracle quote for `PriceGuard.PricePair` and compute the time-weighted average price over `PriceGuard.TwapWindowSeconds`.
2. Derive the deviation against the TWAP and publish it as `loyalty_price_guard_deviation_bps`.
3. If the quote age exceeds `PriceGuard.PriceMaxAgeSeconds` or the deviation breaches `PriceGuard.MaxDeviationBps`, the controller freezes adjustments, emits `loyalty_price_guard_fallback_total` (incremented), and reverts to the static `MinBps` rate until fresh data arrives.

In the stale-price case the controller still processes pending rewards, but does so using the frozen `MinBps` coverage ratio so rewards remain predictable:

- **Scenario**: `PriceGuard.TwapWindowSeconds = 7200`, `PriceGuard.MaxDeviationBps = 300`. The incoming quote is 8% above the TWAP and 12 minutes old.
- **Outcome**: The deviation counter jumps to `800` bps, `nhb_oracle_update_age_seconds` plateaus above the freshness threshold, and the controller logs a price-guard violation. Subsequent coverage calculations revert to `MinBps = 25` bps until a fresh quote resets the guard. Operators can confirm the fallback by correlating `loyalty_price_guard_deviation_bps`, `loyalty_price_guard_fallback_total`, and the dashboard panel tracking `loyalty_prorate_ratio`.
