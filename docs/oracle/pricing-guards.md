# Pricing Guard Reference

Two things carry the "price guard" name in the code. Only the first is on a
live path.

## 1. Loyalty price guard (live)

The dynamic loyalty controller needs a ZNHB/USD price to turn a USD-denominated
daily budget into ZNHB. `Manager.GetRemainingDailyBudgetZNHB`
(`core/state/loyalty_engine.go`) resolves it like this:

1. If `PriceGuard.Enabled` is `false`, the price is treated as exactly `1`.
2. Otherwise it reads the **last accepted swap price proof** for the base
   token of `PricePair` (default base `ZNHB`) from `swap/oracle/last/<BASE>`.
   That record is written each time a `TxTypeSwapVoucherMint` passes price
   proof verification ([`docs/swap/oracle-verification.md`](../swap/oracle-verification.md)).
3. If no record exists, or its timestamp is older than `PriceMaxAgeSeconds`,
   the price is unavailable. Then:
   * with `UseLastGoodPriceFallback = true`, the last stored price is used
     regardless of age and the budget result carries a
     `last_good_price` fallback signal;
   * otherwise the budget is `0`.
4. If the fallback minimum emission is greater than zero and the computed budget
   is `0` or below it, the budget is raised to that minimum and a
   `min_emission` fallback signal is set (unless a `last_good_price` signal is
   already set).

Only `Enabled`, `PricePair` (base token), `PriceMaxAgeSeconds`,
`UseLastGoodPriceFallback` and the fallback minimum emission are read on this
path. `TwapWindowSeconds` and the maximum deviation are accepted, defaulted and
validated as configuration, but `resolveLoyaltyPrice` does not use them.

### Configuration

Section `[global.Loyalty.Dynamic.PriceGuard]` in `config.toml`. Defaults
applied when a value is empty or zero are in `native/loyalty/params.go`:

| Field | Default constant | `config.toml` value |
| --- | --- | --- |
| `Enabled` | - | `true` |
| `PricePair` | `ZNHB/USD` | `ZNHB/USD` |
| `TwapWindowSeconds` | `3600` | `7200` |
| `MaxDeviationBPS` | `500` | `300` |
| `PriceMaxAgeSeconds` | `900` | `600` |
| `FallbackMinEmissionZNHBWei` | - | `""` |
| `UseLastGoodPriceFallback` | - | `true` |

Naming: the TOML keys above are the ones `config.toml` uses
(`config/types.go`). `config/global.go` converts them into the runtime
parameters in `native/loyalty/global.go`, where the same two fields are named
`MaxDeviationBps` and `FallbackMinEmissionZNHB` (the latter is the parsed
value of `FallbackMinEmissionZNHBWei`).

Governance can change the price guard through these parameter keys
(`native/governance/types.go`, all listed in `[governance].AllowedParams` in
`config.toml`): `loyalty.dynamic.priceGuard.pricePair`, `.enabled`,
`.twapWindowSeconds`, `.priceMaxAgeSeconds` and `.maxDeviationBps`. There is
no governance key for `UseLastGoodPriceFallback` or the fallback minimum
emission.

## 2. `core/pricing` package (not wired in)

`core/pricing/pricefeed.go` defines `PriceFeed.GetZNHBUSD(now)`, which queries
a `swap.TWAPOracle` with base `USD` and quote `ZNHB`, returns the rate as a
Q64.64 integer (`PriceQ64`), the age in whole seconds, and a status:

* `ok` - fresh and within the deviation band;
* `stale` - the quote timestamp is older than `PriceMaxAgeSeconds` (a zero
  timestamp also counts as stale);
* `deviant` - the spot rate differs from the oracle's TWAP average by more than
  `MaxDeviationBps` (checked only when the quote is not stale and
  `MaxDeviationBps > 0`; a TWAP error is ignored).

No non-test code imports `nhbchain/core/pricing`, so this logic is not part of
the running node.
