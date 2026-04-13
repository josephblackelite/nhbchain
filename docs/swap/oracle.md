# Swap Oracle Operations

The swap oracle aggregates price updates from the authority feeders and derives a time-weighted average price (TWAP) that anchors mint quotes and audit exports.

## Feeder roles

- **Primary feeders** push signed samples from custody pricing systems. They are onboarded via the operator allow-list and appear under the `Allow` array in `swap.ProviderStatus`.
- **Fallback feeders** (e.g. CoinGecko) provide redundancy. They remain configured but only surface when primary feeds deviate or go dark.
- **Manual feed** is a circuit-breaker used during incidents. It can be injected via `SetSwapManualQuote` and is always the lowest priority.

### Signer rotation

1. Provision the replacement KMS keypair and record the address using the `SignerHistory` tooling.
2. Update the payments gateway configuration (`MinterKMSEnv`) and reload the service; the rotating signer wrapper emits an audit log.
3. Call `swap_provider_status` and confirm the `oracleFeeds` metadata contains fresh observations from the new signer.
4. Remove access to the previous signer and archive the rotation runbook in the security vault.

### Liveness & health reporting

- Every accepted quote updates the in-memory observation history. `swap_provider_status` exposes the last observation timestamp and sample count per `BASE/QUOTE` pair.
- `LastOracleHealthCheck` reflects the timestamp of the most recent on-chain mint quote that passed freshness validation.
- Health monitors should alarm when:
  - A feed reports zero observations for longer than the configured TTL (`MaxQuoteAgeSeconds`).
  - `oracleFeeds[n].observations` stops increasing while mints continue.
  - The TWAP window contains fewer than three samples.

## TWAP persistence

- The aggregator maintains a rolling window defined by `swap.TwapWindowSeconds` (default 300s) and stores up to `swap.TwapSampleCap` samples per pair.
- Every mint ledger entry now includes:
  - `twapRate` – TWAP rendered to 18 decimal places.
  - `twapWindowSeconds`, `twapObservations`, `twapStart`, `twapEnd` – the audit metadata for the window.
- RPC responses (`swap_voucher_get` / `swap_voucher_list`) include the same TWAP fields, enabling downstream risk engines to cross-check quotes.

## Freshness & deviation controls

- `MaxQuoteAgeSeconds` caps quote staleness. Quotes older than the cap are rejected before minting.
- The payments gateway median oracle enforces per-feed TTLs, maximum deviation percentages, and a rate-of-change circuit breaker before forwarding samples to the chain aggregator.

## Operational checklist

- Monitor `swap_provider_status` for `oracleFeeds` drift and stale `LastOracleHealthCheck` values.
- Rotate signer keys quarterly or after any suspected compromise following the steps above.
- Persist feeder health dashboards and include TWAP statistics in treasury audit packages.
