# Performance Tuning Guide

This guide captures tuning levers that help operators meet or exceed the performance baselines for nhbchain services.

## Consensus nodes

- **Database tuning.**
  - Enable `GODEBUG=memprofilerate=0` when profiling to reduce allocation overhead during benchmarking.
  - Configure RocksDB or the underlying KV store with high-compaction trigger thresholds to avoid background stalls.
  - Place state and WAL directories on separate NVMe volumes where possible.
- **Mempool sizing.** Set `global.mempool.MaxBytes` to at least 64 MB on high-throughput validators. Monitor eviction rates (`mempool_evicted_total`).
- **P2P networking.** Increase the inbound/outbound peer limits in `config.toml` for data centers with sufficient bandwidth, but keep them symmetric to prevent gossip imbalances.

## Gateway services

- **Connection pools.** Tune database and RPC client pools based on p95 latency. Start at 2× CPU cores and adjust after load testing.
- **Caching.** Configure HTTP response caching for idempotent endpoints (account lookups, rate tables). Validate cache TTLs against compliance requirements.
- **Async processing.** Offload blocking operations (file exports, invoicing) to background workers to keep API latency predictable.

## Observability-driven tuning

1. **Profile hotspots.** Use `pprof` or `go tool trace` during load tests to identify CPU or lock contention.
2. **Adjust parameters.** Modify relevant configuration (mempool size, worker counts, queue depths) and document the change.
3. **Measure impact.** Re-run the benchmark and compare to the baselines recorded in `docs/perf/baselines.md`.
4. **Roll out carefully.** Apply changes to staging first, monitor for 24 hours, then promote to production with a rollback plan.

## Capacity planning

- Run quarterly load tests using the `tests/load/` scenarios.
- Capture TPS, latency, and resource usage across consensus, gateway, and database layers.
- Update the operations runbooks with new resource requirements (CPU, RAM, storage) when sustained usage approaches 70% of capacity.
