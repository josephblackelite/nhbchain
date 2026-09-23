# Tuning levers

Settings in this repository that bound node throughput and resource use. Values
are defaults from `config/config.go`; the repository `config.toml` overrides some
of them.

## Mempool and blocks

| Setting | Default | Effect |
| --- | --- | --- |
| `[mempool] MaxTransactions` | 4000 (`DefaultMempoolMaxTransactions`; `config.toml` sets 5000) | Maximum pending transactions; further admissions fail with `mempool full` (`-32030`). `AllowUnlimited = true` allows 0 (no limit). |
| `[global.Mempool] MaxBytes` | 16 MiB (`16 << 20`) | Byte cap on pending transactions. |
| `[global.Mempool] POSReservationBPS` | 1500 | Share of each block reserved for POS-tagged transfers ([QoS](../specs/pos-qos.md)). |
| `[global.Blocks] MaxTxs` | 500 | Maximum transactions per block; enforced when proposing and when validating a block. |

## Block production containment

A proposer builds each block from a copy of committed state in waves
(`core/proposal_containment.go`): every wave runs the candidate transactions,
collects all failures, and the survivors run again from a fresh copy; a panic in
one transaction is recovered; when the budget or the wave cap runs out, the clean
prefix of the last wave is used, and failing that an empty block is built. This is
local to the proposer (validators re-execute the block as before and reject it on
any error) and nothing in it changes a consensus rule. Transactions that fail are
recorded in a bounded strike book and evicted from the local mempool after repeated
failures or a time limit. Defaults (`defaultBuildConfig`):

| Knob | Default | Environment override |
| --- | --- | --- |
| Build budget for all waves of one block | the smaller of 1 s and half of `[consensus] ProposalTimeout` (`SetProposalBuildBudget`; 1 s at the default 2 s) | `NHB_PROPOSAL_BUILD_BUDGET_MS` (pins the value) |
| Waves per build | 6 | `NHB_PROPOSAL_MAX_WAVES` |
| Failures across builds before eviction | 3 | `NHB_POISON_STRIKES` |
| Time a transaction that was only skipped stays before eviction | 2 h | `NHB_POISON_SKIP_TTL` (seconds) |
| Time before any transaction with a failure record is evicted | 24 h | none |
| Lease hiding a transaction offered in a proposal | 30 s | `NHB_INFLIGHT_LEASE_SECS` |
| No-commit interval that counts as a stall | 60 s | `NHB_LIVENESS_STALL_SECS` |

`NHB_PROPOSER_EXCLUDE_TXTYPES` is an operator deny-list of transaction types this
proposer will not include, and `NHB_ALLOW_CLOCK_DEPENDENT_TXS` disables the
wall-clock read detector; both are read once when the node starts. A malformed
value is ignored and reported as a start-up warning. The metrics and alerts for this
are listed in [baselines](./baselines.md#prometheus-metrics).

## RPC

`RPCMaxTxPerWindow` and the `RPCMaxTxPer*` limits (default 5 per
`RPCRateLimitWindow` = 60 s), `RPCRouteRateLimits` per method, and the RPC
timeouts (`RPCReadHeaderTimeout` 10 s, `RPCReadTimeout` 15 s, `RPCWriteTimeout`
15 s, `RPCIdleTimeout` 120 s in the config file the node writes when none exists;
0 means no timeout, which is what the repository `config.toml` sets) are described
in [rpc.md](../api/rpc.md#rate-limits).

The public reads that scan blocks or hold the state lock share a small admission
pool, sized by `RPCQueryMaxConcurrent` (default half the CPUs, at least 1 and at most
2), `RPCQueryMaxPerClient` (1), `RPCQueryQueueDepth` (8), `RPCQueryQueueWaitMS` (100)
and `RPCQueryTimeoutSeconds` (10); `RPCDisableExplorerLoop` stops the per-block
explorer snapshot rebuild on a node that serves no public RPC. The WebSocket streams
are capped by `RPCWebSocketMaxConnections` (256) and `RPCWebSocketMaxPerIP` (8). See
[RPC query limits](../ops/rpc-query-limits.md).

## P2P

`MaxPeers` (64), `MaxInbound`, `MaxOutbound`, `MinPeers`, `OutboundPeers`,
`MaxMsgsPerSecond` (32, per peer), `[p2p] Burst` (200), `MaxMsgBytes` (1 MiB),
`ReadTimeout` (90 s), `WriteTimeout` (5 s). See [P2P](../overview/p2p.md).

## Storage

Node state is stored in LevelDB (`storage/db.go` uses go-ethereum's
`ethdb/leveldb`); the data directory is `DataDir` (default `./nhb-data`).

## Measuring a change

Use the benchmarks and metrics in [baselines](./baselines.md): run the benchmark
or load generator before and after the change and compare the same metrics.
