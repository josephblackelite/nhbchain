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
