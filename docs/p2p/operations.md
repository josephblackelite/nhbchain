# Operational Guidance

Day-to-day notes for running a P2P node. Settings are in the `[p2p]` section of
`config.toml`. More detail is in [networking operations](../networking/ops.md).

## Bootnodes and persistent peers

- `Bootnodes` and `PersistentPeers` are lists of `host:port` strings. The server
  trims, de-duplicates and sorts them, dials every entry at start-up, and
  retries failed or dropped connections with a delay that starts at
  `DialBackoffSeconds` and doubles up to one minute. The repo `config.toml` ships
  both lists empty; the validator bootstrap script writes its bootnode into both.
- Addresses in either list are treated as persistent: exempt from rate limits,
  the per-address inbound limits and the chain-data request budgets, and from
  connection-manager pruning, and not banned for score or on handshake evidence
  (an operator can still ban them with `net_ban`).

## Firewalls and NAT

- Allow inbound TCP on the port in `ListenAddress`, and outbound TCP to peers. The
  P2P layer uses TCP only.
- Behind NAT, forward the port yourself and set `ExternalAddress` (`host:port`) so
  your handshake advertises a dialable address. The node never attempts UPnP.

## Monitoring and logging

- The server logs new and closed connections, rate-limit hits, refused inbound
  connections, chain-data requests dropped over budget, bans and handshake
  failures; message names are listed in
  [networking security](../networking/security.md#log-messages).
- `p2p_info` returns peer counts, configured limits, the local node ID, bootnodes,
  persistent peers and the merged seed list.
- `p2p_peers` (same result as `net_peers`; both need the RPC bearer token,
  because the list names every peer's address, score and ban state) returns each
  known peer's address, direction, state, score, failure count and ban expiry. Use
  it to find peers with negative scores or repeated failures.
- Metrics are listed in [networking observability](../networking/observability.md).

## Configuration changes

The configuration is read at start. Restart the node after changing rate limits,
thresholds or peer lists.
