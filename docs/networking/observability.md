# Networking observability

The P2P server now publishes richer observability data through Prometheus gauges/counters and optional OpenTelemetry metrics.

## Prometheus metrics

| Metric | Type | Labels | Description |
| ------ | ---- | ------ | ----------- |
| `nhb_p2p_peer_score` | gauge | `peer` | Current composite reputation score after decay. |
| `nhb_p2p_peer_latency_ms` | gauge | `peer` | Exponential moving average of ping round-trip latency. |
| `nhb_p2p_peer_useful_events` | gauge | `peer` | Count of useful protocol messages processed. |
| `nhb_p2p_peer_misbehavior` | gauge | `peer` | Count of misbehavior incidents (malformed payloads, rate-limit breaches). |
| `nhb_p2p_handshakes_total` | counter | `result` | Handshake outcomes (success/failure). |
| `nhb_p2p_gossip_messages_total` | counter | `direction`, `type` | Gossip/control messages sent or received, keyed by message type byte. |

Metrics are registered once and shared across server instances.

## OpenTelemetry

If an OpenTelemetry SDK is configured, the server emits matching counters/histograms (`nhb.p2p.handshakes`, `nhb.p2p.gossip`,
`nhb.p2p.latency_ms`). When OTEL instrumentation is unavailable the server falls back to no-op exporters.

## Runtime hooks

* Handshake success/failure increments the handshake counter.
* Every message send/receive increments the gossip counter.
* Ping/pong responses update latency EWMAs.
* Useful control/data messages increment the usefulness gauge; malformed or rate-limited traffic increments the misbehavior gauge.

Use the `sync_status` RPC or Prometheus metrics to monitor fast-sync progress and peer health during operations. The connection
manager now prefers pruning peers with repeated misbehavior, high latency, or low usefulness.
