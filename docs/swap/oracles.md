# Oracle Aggregation

The swap oracle aggregates multiple upstream price feeds and derives a defended median that anchors mint decisions.

## Sources

The service currently ships adapters for:

- **NOWPayments** – integrates the hosted swap quote API, authenticated via API key.
- **CoinGecko** – queries the public `simple/price` endpoint with optional asset symbol remapping.

Additional sources can be added by implementing the `oracle.Source` interface and registering it in the adapter registry.

## Aggregation Loop

1. Poll each configured source on the defined interval.
2. Filter out quotes that are stale, negative, or in the future.
3. Persist individual observations to `oracle_samples` for auditability.
4. Compute a median across valid quotes and store the result in `oracle_snapshots`.
5. Emit a proof hash that combines the pair, timestamp, and participating feeds.
6. Forward the update to consensus for on-chain consumption.

The manager enforces `min_feeds` before a snapshot is considered valid. Operators should configure at least two reliable feeds
with overlapping coverage to avoid gaps.

## TWAP / History

While the current release focuses on median fan-in, the database schema retains sufficient raw data to derive TWAP windows off-line.
Downstream analytics can reconstruct time-weighted averages or export CSV extracts straight from the SQLite file for compliance.
