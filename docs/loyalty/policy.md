# Loyalty Dynamic Policy Defaults

The loyalty engine ships with an adaptive controller that gently adjusts the base reward rate over time. The following table summarises each dynamic field and the default values bundled with the node binary.

| Field | Description | Default |
| --- | --- | --- |
| `TargetBps` | Desired long-run basis-point rate the controller attempts to converge to when activity stabilises. | `5,000` bps |
| `MinBps` | Lower bound of the permitted basis-point band for automatic adjustments. | `3,000` bps |
| `MaxBps` | Upper bound of the permitted basis-point band for automatic adjustments. | `7,000` bps |
| `SmoothingStepBps` | Maximum change (in basis points) applied per adjustment cycle; smaller values produce gradual moves. | `50` bps |
| `CoverageWindowDays` | Rolling window (in UTC days) of settlement activity observed before recomputing the dynamic rate. | `7` days |
| `DailyCapWei` | Network-wide soft cap on ZNHB minted through dynamic boosts each day, expressed in wei. A value of `0` disables the cap. | `0` wei |
| `YearlyCapWei` | Network-wide soft cap on ZNHB minted through dynamic boosts each year, expressed in wei. A value of `0` disables the cap. | `0` wei |
| `PriceGuard.Enabled` | Toggles price sanity checks when consuming oracle data to estimate coverage ratios. | `false` |
| `PriceGuard.MaxDeviationBps` | Maximum tolerated oracle variance relative to the rolling average before adjustments are frozen. | `500` bps |

Operators can override these settings in `config.toml` under the `[global.loyalty.Dynamic]` section. Leave any field unset (or zero) to continue using the compiled defaults above.
