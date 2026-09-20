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

## RPC

`RPCMaxTxPerWindow` and the `RPCMaxTxPer*` limits (default 5 per
`RPCRateLimitWindow` = 60 s), `RPCRouteRateLimits` per method, and the RPC
timeouts (`RPCReadHeaderTimeout` 10 s, `RPCReadTimeout` 15 s, `RPCWriteTimeout`
15 s, `RPCIdleTimeout` 120 s by default) are described in
[rpc.md](../api/rpc.md#rate-limits).

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
