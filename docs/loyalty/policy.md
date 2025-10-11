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
| `PriceGuard.Enabled` | Toggles price sanity checks when consuming oracle data to estimate coverage ratios. | `false` |
| `PriceGuard.PricePair` | Oracle trading pair queried when evaluating coverage. | `ZNHB/USD` |
| `PriceGuard.TwapWindowSeconds` | TWAP smoothing window applied to the oracle pair before computing deviations. | `3,600` seconds |
| `PriceGuard.MaxDeviationBps` | Maximum tolerated oracle variance relative to the rolling average before adjustments are frozen. | `500` bps |
| `PriceGuard.PriceMaxAgeSeconds` | Maximum age of oracle data before the controller halts adjustments. | `900` seconds |

Operators can override these settings in `config.toml` under the `[global.loyalty.Dynamic]` section. Leave any field unset (or zero) to continue using the compiled defaults above.

## Yearly emission cap

The yearly cap limits how much ZNHB the loyalty engine may emit across base and program rewards within a calendar year. The cap is derived from the configured `YearlyCapPctOfInitialSupply` percentage and the initial ZNHB supply. Each time a reward is applied the engine increments the year-to-date counter. When an emission would exceed the yearly cap the payout is rejected, the counter remains unchanged, and a `loyalty.cap.hit` event is emitted with the attempted amount, configured cap, cumulative emissions, and the remaining headroom (`0` once saturated). Exact matches to the cap are permitted and flagged with the same event so operators can prepare replenishment or governance actions.
