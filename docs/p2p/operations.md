# Operational Guidance

This guide summarises day-to-day tasks for operating NHB P2P nodes.

## Bootnodes and persistent peers

* Configure canonical discovery endpoints via `[p2p].Bootnodes`. The server dials
  every entry on start-up and reattempts failed connections with exponential
  backoff (capped at 1 minute). Ship production configurations with
  `Bootnodes = []` and let operations teams provide approved endpoints.
* Use `[p2p].PersistentPeers` for validators or trusted relays that should be
  kept connected. Persistent peers are retried indefinitely and never banned,
  although they may be greylisted when they misbehave.
* Bootnode and persistent-peer addresses are deduplicated and normalised at
  start-up. Use `host:port` pairs for TCP connections.

## Firewalls and NAT

* Open the configured `ListenAddress` TCP port on inbound firewalls.
* If running behind NAT, port-forward the listener to ensure other nodes can
  reach the instance. Failing to do so may result in an outbound-only node.
* Outbound connections originate from ephemeral ports; ensure the firewall
  permits established TCP flows.

## Monitoring and logging

* The P2P server emits log lines for new connections, disconnections, rate-limit
  violations, greylist/bans, and handshake failures. Integrate these logs into
  your observability stack.
* Query `p2p_info` periodically to confirm peer counts, limits, and the local
  node identity (`self`).
* Query `p2p_peers` to inspect per-peer reputation, direction (inbound/outbound),
  first/last seen timestamps, and remote addresses. This is useful for
  identifying abusive peers.

## Configuration refresh

Changes to `config.toml` require a node restart. When tweaking rate limits or
reputation thresholds, deploy incrementally and monitor the RPC endpoints to
confirm the desired effect.
