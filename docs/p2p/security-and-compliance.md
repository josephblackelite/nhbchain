# P2P Security Controls

The controls the P2P layer enforces, and the evidence an operator can collect. All
behavior is from `p2p/`; see [networking security](../networking/security.md) for
the handshake digest and ban table.

## Controls in the code

- **Chain and genesis pinning.** The handshake carries `chainId` and
  `genesisHash`; a mismatch closes the connection and bans the peer for
  `PeerBanDuration`.
- **Signed node identity.** Each handshake frame is signed with the node's
  secp256k1 key over a digest that includes the chain ID, genesis hash, a random
  12-byte nonce and the node ID. The verifier recovers the key and checks it
  against the claimed node ID. The node key is separate from any wallet or
  validator key and needs no funds.
- **Replay guard.** A handshake nonce seen again within 10 minutes (per process,
  up to 100,000 entries) is rejected and the peer is banned. There is no
  timestamp in the handshake, so a recorded frame is accepted again once its entry
  has expired or the process has restarted.
- **Reputation.** Protocol violations, rate-limit hits and slow peers lower a
  peer's score; low enough scores greylist and then ban it
  ([reputation](reputation.md)).
- **Rate limits.** Per-IP, per-peer and global token buckets
  ([rate limits](ratelimits.md)).
- **Size limit.** A frame larger than `MaxMsgBytes` (1 MiB by default) is a
  protocol violation.
- **Persistent peers.** Configured bootnodes and persistent peers are exempt from
  rate limits and score-based bans, so a misconfigured or compromised persistent
  peer is not throttled by the node.
- **Connection caps.** `MaxPeers`, `MaxInbound` and `MaxOutbound` are enforced at
  registration.

The P2P transport itself is unencrypted TCP; message confidentiality is not
provided by this layer.

## Evidence an operator can collect

- **Configuration.** Keep `config.toml` (the `[p2p]` section in particular) in
  version control.
- **RPC snapshots.** `p2p_info` (counts, limits, node ID, bootnodes, persistent
  peers, seeds) and `net_peers` / `p2p_peers` (per-peer state, score, failures,
  ban expiry) show the current peer set and enforcement state. Neither returns
  first-seen or greylist fields.
- **Logs.** Connections, disconnections, rate-limit hits, bans and handshake
  failures are logged by the P2P server; retain them (see
  [log messages](../networking/security.md#log-messages)).
- **Metrics.** `nhb_p2p_handshakes_total`, `nhb_p2p_peer_score`,
  `nhb_p2p_peer_misbehavior` and the others in
  [observability](../networking/observability.md).
- **Node identity.** `<DataDir>/p2p/node_key.json` holds the node's private key
  (mode `0600`); protect it like any other key file. Deleting it gives the node a
  new node ID.
