# Performance measurement

This repository does not contain measured baseline results. The audit plan
`ops/audit/perf.yaml` records two expectations, not measurements: ledger
insertions stay below 5 ms median latency under benchmark load, and governance
apply commits keep p95 finality latency within the configured SLA at about 1,000
tx/min synthetic load. The tools and metrics that exist for measuring the node
are listed here; record your own baselines from them.

## Benchmarks

* `make audit:perf` runs `go test -run '^$' -bench=. -benchtime=100x -benchmem
  ./tests/perf/...` and writes `artifacts/perf/bench.txt`. The benchmarks are
  `BenchmarkLedgerPut` (`tests/perf/perf_test.go`, an in-memory swap-ledger
  benchmark) and `BenchmarkConsensusFinalityLatency`
  (`tests/perf/consensus_latency_test.go`). The latter drives the in-process stub
  cluster from `tests/support/cluster` (see [e2e testing](../testing/e2e.md)) and
  writes `consensus_latency_report.json` and `.txt`; it skips under `-short`.
  The Makefile target then calls `scripts/audit/run_phase.sh perf
  ops/audit/perf.yaml artifacts/perf --hash artifacts/perf/bench.txt --hash
  artifacts/perf/consensus_latency_report.json`; `ops/audit/perf.yaml` is tracked
  in the repository.
* `bench/posloader` is a POS-transaction load generator. Flags: `--rpc` (default
  `http://127.0.0.1:8545`), `--key` (hex private key, or `POSLOADER_KEY`),
  `--rate` (transactions per minute, default 600), `--duration` (default 2m),
  `--intent-prefix` (default `pos-load`). It requires `NHB_RPC_TOKEN` and submits
  through `nhb_sendTransaction`.

## Prometheus metrics

Metrics registered in `observability/metrics.go` (all under the `nhb` namespace):

| Metric | Meaning |
| --- | --- |
| `nhb_consensus_block_interval_seconds` | Gauge: seconds between timestamps of consecutive committed blocks. |
| `nhb_mempool_pos_lane_fill` | Gauge: POS backlog divided by reserved POS slots ([QoS](../specs/pos-qos.md)). |
| `nhb_mempool_pos_lane_backlog{asset}` | Gauge: pending POS-tagged transfers by asset. |
| `nhb_mempool_pos_tx_enqueued_total` | Counter of POS-tagged transactions admitted. |
| `nhb_mempool_pos_p95_finality_ms` | Histogram of POS enqueue-to-finality latency in ms (buckets 50 to 12800). |
| `nhb_rpc_limiter_hits_total` | RPC rate-limiter hits. |
| `nhb_module_requests_total`, `nhb_module_errors_total`, `nhb_module_request_duration_seconds`, `nhb_module_throttles_total` | Per RPC module/method request statistics. |
| `nhb_pos_auth_expired_total` | POS authorizations voided by expiry sweeps. |
| `nhb_staking_*`, `nhb_loyalty_*`, `nhb_paymaster_*`, `nhb_token_supply_total`, `nhb_security_insecure_binds_total` | Module-specific gauges and counters. |
