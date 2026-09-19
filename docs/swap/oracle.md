# Swap Oracle Aggregator

`native/swap/oracle.go` contains an in-process `OracleAggregator`. This page
describes what it does and, just as important, where it is and is not used in
the current code.

## Where it is used

* `cmd/nhb/main.go` and `cmd/consensusd/main.go` each build one aggregator
  from the `[swap]` config, register three sources (`manual`, `nowpayments`,
  `coingecko`), and hand it to the node with `node.SetSwapOracle` (and the
  manual source with `node.SetSwapManualOracle`).
* Only `cmd/nhb/main.go` seeds the manual source (`SetDecimal` calls, see the
  table below). `cmd/consensusd/main.go` does not seed it and does not read
  `NHB_ZNHB_ORACLE_PRICE_USD`, so under `consensusd` the manual source is
  empty until quotes are set through `swap_setManualQuote`.
* The only consumers of that aggregator in non-test code are
  `Node.SwapProviderStatus` (which calls its `Health()` method) and the
  unused `core/pricing` package (see
  [`docs/oracle/pricing-guards.md`](../oracle/pricing-guards.md)). No code path
  calls `OracleAggregator.GetRate` during block execution or RPC handling.
* Voucher mints do not use it. They are priced from the signed price proof
  ([oracle-verification.md](oracle-verification.md)).

## Aggregation behaviour

`GetRate(base, quote)` (`native/swap/oracle.go`):

1. Iterates the configured sources in `[swap].OraclePriority` order. Sources
   that are unregistered, return an error, return a non-positive rate, or
   return a quote older than `MaxQuoteAgeSeconds` are skipped.
2. Returns the **first** fresh quote. It is a priority fallback, not a median.
3. Records the returned quote in an in-memory per-pair history.

`ErrNoFreshQuote` (`swap: no fresh oracle quote available`) is returned when
nothing qualifies.

Registered sources (`cmd/nhb/main.go`; `cmd/consensusd/main.go` registers the
same three but seeds nothing):

| Name | Behaviour |
| --- | --- |
| `manual` | In-memory quotes set by `swap_setManualQuote`. Seeded at startup with `USD/NHB = 1.0` and `USD/ZNHB` from `NHB_ZNHB_ORACLE_PRICE_USD` (default `0.05`). |
| `nowpayments` | HTTP adapter for an external exchange-rate endpoint (`NewNowPaymentsOracle`). |
| `coingecko` | HTTP adapter with an **empty** asset-ID map, so `NHB` and `ZNHB` quotes fail as unmapped and the aggregator falls through to the next source (comment in `cmd/nhb/main.go`). |

A fourth HTTP adapter constructor exists in `native/swap/oracle.go` but is not
registered by either binary.

## TWAP

`TWAP(base, quote, window)` returns the **arithmetic mean** of the samples in
the window (it is not weighted by time between samples), plus the median, the
sample count, start and end times, the contributing sources, and a SHA-256
proof ID. The window and sample cap come from `[swap].TwapWindowSeconds`
(`config.toml`: `300`) and `[swap].TwapSampleCap` (default `128`). The history
lives in memory and is lost on restart.

## Configuration

| Key | Default in `Config.Normalise` | `config.toml` |
| --- | --- | --- |
| `AllowedFiat` | `["USD"]` | `["USD", "EUR", "GBP"]` |
| `MaxQuoteAgeSeconds` | `120` | `120` |
| `SlippageBps` | `50` | `50` |
| `OraclePriority` | `["manual"]` | `["nowpayments", "coingecko", "manual"]` |
| `TwapWindowSeconds` | `0` (a negative value becomes `0`) | `300` |
| `TwapSampleCap` | `128` | `128` |
| `PriceProofMaxDeviationBps` | `100` | `0` (resolves to `100`) |
| `PayoutAuthorities` | `["treasury"]` | `["treasury"]` |

## Provider status

`swap_provider_status` (JWT required, no parameters) returns:

```json
{
  "allow": ["provider-id"],
  "lastOracleHealthCheck": 0,
  "oracleFeeds": [
    {"Pair": "USD/ZNHB", "Base": "USD", "Quote": "ZNHB", "LastObservation": 1734000000, "Observations": 3}
  ]
}
```

* `allow` is `[swap.providers].Allow`, lower-cased and de-duplicated.
* `oracleFeeds` is included only when the aggregator has recorded samples. The
  field names are capitalised because `swap.OracleFeedStatus` has no JSON tags.
  Since nothing calls `GetRate`, the list is empty in practice.
* `lastOracleHealthCheck` is read from `Node.swapOracleLast`, which is only
  written by `Node.recordSwapOracleHealth`. That function has no callers, so
  the value stays `0`.

## Manual quote

`swap_setManualQuote` (JWT required) sets a quote on the manual source. See
[admin.md](admin.md). It affects only the in-process aggregator.

## Signer rotation

Price-proof signers live on-chain, not in this aggregator. Rotate them with a
`policy.swapPriceSigner` governance proposal
([oracle-verification.md](oracle-verification.md#signer-management)).
