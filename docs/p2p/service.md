# p2pd Service Overview

`p2pd` provides the external peer-to-peer networking surface for NHB Chain.
It exposes an internal gRPC interface that `consensusd` consumes while
speaking QUIC/TLS to the wider validator mesh.

## Seed Discovery

Seed peers are composed from static configuration (`config.toml`) and dynamic
registry entries. The daemon normalises entries, deduplicates case-insensitively
and records the origin for operator introspection. Additional sources (such as
on-chain registries) are merged through the `seeds` resolver package.

## Peer Scoring & Enforcement

The `ServerConfig` derived from `config.toml` tunes peer behaviour:

* **Rate limits** – `RateMsgsPerSec` and `Burst` protect against floods.
* **Ban/Grey scores** – peers accumulate penalties for misbehaviour which drive
  temporary disconnects.
* **Handshake & Ping timeouts** – guard the transport handshake and ongoing
  liveness checks.
* **Max inbound/outbound counts** – ensure the node honours validator capacity
  policies.

Peer metadata is persisted in the peerstore (`$DATA_DIR/p2p/peerstore`),
allowing scoring decisions to survive restarts.

## Transport

* **QUIC + TLS** – default secure transport for peer connections.
* **gRPC** – internal control plane consumed by `consensusd` on `:9091` by default.
* **Retry/backoff** – outbound dials honour the configured `DialBackoffSeconds`.

`p2pd` also exposes a gRPC relay used by consensus components to publish gossip
and read heartbeats. The relay now cancels its send loop as soon as a terminal
error occurs, closing the stream and preventing consensus-side deadlocks. When
this happens `p2pd` emits structured error logs for failed broadcasts and a
warning when an unknown envelope is received, helping operators correlate
disconnects with upstream causes.

## Health & Operations

* gRPC server on `:9091` (configurable) for internal RPCs.
* Promotes heartbeats to consensus for liveness accounting.
* Emits structured logs on peer connect/disconnect, scoring changes and seed
  hydration events.

See the [failover runbook](../runbooks/p2pd-failover.md) for operational
checklists.
