# Networking observability

The P2P server publishes Prometheus metrics and OpenTelemetry instruments
(`p2p/metrics.go`). The metrics are registered once per process and shared by all
server instances.

## Prometheus metrics

| Metric | Type | Labels | Description |
| ------ | ---- | ------ | ----------- |
| `nhb_p2p_peer_score` | gauge | `peer` | Reputation score after decay, updated whenever the peer's status is recomputed. |
| `nhb_p2p_peer_latency_ms` | gauge | `peer` | Exponentially weighted moving average (weight 0.2 for the newest sample) of ping/pong latency in milliseconds. |
| `nhb_p2p_peer_useful_events` | gauge | `peer` | Count of messages processed as useful (`MarkUseful`). |
| `nhb_p2p_peer_misbehavior` | gauge | `peer` | Count of misbehavior incidents (protocol violations and rate-limit hits, `MarkMisbehavior`). |
| `nhb_p2p_handshakes_total` | counter | `result` | Handshake outcomes: `success` or `failure`. |
| `nhb_p2p_gossip_messages_total` | counter | `direction`, `type` | Messages read (`in`) or written (`out`), labelled by the message type byte as `0xNN` (for example `0x09` for Ping). |
| `nhb_p2p_nonce_guard_size` | gauge | none | Entries in the handshake nonce guard. |
| `nhb_p2p_nonce_guard_evicted_total` | counter | none | Nonce-guard entries evicted by age or capacity. |

The per-peer series are deleted when the peer disconnects
(`networkMetrics.removePeer`).

The gossip relay between `p2pd` and `consensusd` (`network/metrics.go`) adds
`nhb_network_relay_queue_enqueued_total`, `nhb_network_relay_queue_dropped_total`
and `nhb_network_relay_queue_occupancy`.

## Validator liveness metrics

These metrics are not part of the P2P layer; they are registered in
`observability/metrics.go` and recorded by the node's liveness watchdog
(`core/liveness_watchdog.go`) and block-building code (`core/build_telemetry.go`).
The `nhb` and `consensusd` binaries expose the Prometheus registry at `/metrics`
only when `NHB_METRICS_ADDR` is set to a listen address (`host:port`); the endpoint is unauthenticated, a warning is logged when it
is bound to a non-loopback address, and a failure to start it never stops the
validator (`observability/metrics_server.go`).

| Metric | Type | Description |
| ------ | ---- | ----------- |
| `nhb_consensus_seconds_since_last_commit` | gauge | Seconds since this node last saw its committed height advance (process start counts as a commit). Sampled every 5 seconds. |
| `nhb_consensus_last_commit_height` | gauge | Committed height as last observed by the watchdog. |
| `nhb_consensus_liveness_stalled` | gauge | 1 while no block has committed for longer than the stall threshold (60 seconds by default, `NHB_LIVENESS_STALL_SECS`), else 0. A continuing stall is logged as `LIVENESS: no block committed for Ns` once a minute. |
| `nhb_consensus_block_interval_seconds` | gauge | Seconds between the timestamps of consecutive committed blocks. |
| `nhb_consensus_build_failures_consecutive`, `nhb_consensus_build_failures_total{reason}`, `nhb_consensus_empty_block_fallbacks_total`, `nhb_consensus_build_duration_seconds`, `nhb_consensus_build_waves`, `nhb_consensus_tx_panics_recovered_total`, `nhb_consensus_local_validation_failures_total` | mixed | Block-proposal building: failures, fallbacks to an empty block, duration and execution waves. |
| `nhb_mempool_tx_failures_total{disposition}`, `nhb_mempool_evictions_total{reason}`, `nhb_mempool_strike_records`, `nhb_mempool_strike_overflow_evictions_total`, `nhb_mempool_inflight_leases_expired_total` | mixed | Transactions that failed while a proposal was assembled, and mempool evictions by the containment layer. |

`observability/alerts.yaml` has a `consensus-liveness` group with alerts on these
(`ConsensusNoCommit`: `nhb_consensus_seconds_since_last_commit > 60` for a minute;
`ConsensusBuildFailing`: `nhb_consensus_build_failures_consecutive >= 2`).

## OpenTelemetry

The server obtains a meter named `nhbchain/p2p` from the global meter provider
and records `nhb.p2p.handshakes` (counter, attribute `result`),
`nhb.p2p.gossip` (counter, attributes `direction` and `type`) and
`nhb.p2p.latency_ms` (histogram, attribute `peer`). If the instruments cannot be
created it falls back to a no-op meter.

## What updates them

- `initPeer` records a handshake `success` or `failure`.
- Every message written or read increments `nhb_p2p_gossip_messages_total`.
- Ping and Pong messages carry a send timestamp; the receiver updates the latency
  EWMA from it, so the value assumes the peers' clocks agree.
- Each processed message calls `MarkUseful`; protocol violations and rate-limit
  hits call `MarkMisbehavior`.
- `sync_status` reports the fast-sync manager's snapshot height and chain height;
  see [sync.md](sync.md).

The connection manager's prune order uses these same counters: peers with more
misbehavior incidents, lower score, fewer useful messages and higher latency are
disconnected first (see [overview.md](overview.md#connection-manager)).
