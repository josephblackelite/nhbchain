# Pricing Guard Reference

The loyalty controller relies on a guarded ZNHB/USD feed when evaluating coverage
ratios. Guards make the oracle resilient against stale or aberrant readings while
still surfacing the most recent rate as a Q64.64 fixed-point number.

## Status codes

The `core/pricing` package exposes `PriceFeed.GetZNHBUSD(now)` which returns the
latest price observation along with a status flag:

- `ok` &mdash; The quote is fresh and within the configured deviation band.
- `stale` &mdash; The quote timestamp exceeds `PriceMaxAgeSeconds`.
- `deviant` &mdash; The spot price diverges from the rolling TWAP by more than
  `MaxDeviationBps`.

The call also reports the age of the observation (in whole seconds) and a Q64.64
value representing the USD per ZNHB rate. Downstream modules can decide whether
additional fallback behaviour is required when the status is not `ok`.

## Guard knobs

Guard behaviour is controlled by the loyalty price guard configuration:

| Field | Description |
| --- | --- |
| `PricePair` | Oracle pair to query, defaulting to `ZNHB/USD`. |
| `TwapWindowSeconds` | TWAP window supplied to the oracle aggregator. |
| `MaxDeviationBps` | Maximum permitted deviation between the spot rate and the TWAP average (basis points). |
| `PriceMaxAgeSeconds` | Maximum age of a quote before it is marked stale. |

`core/pricing` normalises the guard config before evaluating the oracle so that
unset values fall back to the loyalty defaults.
