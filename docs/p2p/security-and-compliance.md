# P2P Security Controls

The controls the P2P layer enforces, and the evidence an operator can collect. All
behavior is from `p2p/`; see [networking security](../networking/security.md) for
the handshake digest and ban table.

## Controls in the code

- **Chain and genesis pinning.** The handshake carries `chainId` and
  `genesisHash`; a mismatch closes the connection. The node the packet names is
  banned for `PeerBanDuration` only when the packet is signed by that node, and
  never when it is a configured persistent peer.
- **Signed node identity.** Each handshake frame is signed with the node's
  secp256k1 key over a digest that includes the chain ID, genesis hash, a random
  12-byte nonce and the node ID. The verifier recovers the key and checks it
  against the claimed node ID. The node key is separate from any wallet or
  validator key and needs no funds.
- **Replay guard.** A handshake nonce seen again within 10 minutes (per process,
  up to 100,000 entries) is rejected. Nothing is held against the node ID it names,
  since anyone who has seen a handshake can send it again. There is no timestamp in
  the handshake, so a recorded frame is accepted again once its entry has expired
  or the process has restarted.
- **Inbound admission.** Before anything is read from a new inbound connection the
  server limits how fast one address may open connections, how many handshakes it
  may have in flight (4) and how many established inbound connections it may hold
  (8), and how many handshakes may be in flight in all (64). Loopback, hosts that
  are not IP addresses and the addresses of configured bootnodes and persistent
  peers are exempt from the per-address limits (`admitInbound`, `p2p/server.go`).
- **Chain-data request budgets.** `GetBlocks` and `GetStatus` requests have a
  budget per remote address and one shared by all addresses; a request over budget
  is dropped without blaming the peer (`admitRequest`). Answers go to the peer
  that asked.
- **Bounded tables.** The peer book, the peerstore, the reputation table and the
  PEX address book are capped (see [networking overview](../networking/overview.md)
  and [peer exchange](../networking/pex.md)), and PEX addresses are accepted only
  as the reply to a request this node sent.
- **Reputation.** Protocol violations, rate-limit hits and write errors lower a
  peer's score; low enough scores greylist and then ban it
  ([reputation](reputation.md)).
- **Rate limits.** Per-IP, per-peer and global token buckets
  ([rate limits](ratelimits.md)).
- **Size limit.** A frame larger than `MaxMsgBytes` (1 MiB by default) is a
  protocol violation.
- **Persistent peers.** Configured bootnodes and persistent peers are exempt from
  rate limits, per-address limits and score-based or handshake-based bans, so a misconfigured or compromised persistent
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
  ban expiry) show the current peer set and enforcement state; `net_peers` and
  `p2p_peers` need the RPC bearer token. Neither returns first-seen or greylist
  fields.
- **Logs.** Connections, disconnections, rate-limit hits, bans and handshake
  failures are logged by the P2P server; retain them (see
  [log messages](../networking/security.md#log-messages)).
- **Metrics.** `nhb_p2p_handshakes_total`, `nhb_p2p_peer_score`,
  `nhb_p2p_peer_misbehavior` and the others in
  [observability](../networking/observability.md).
- **Node identity.** `<DataDir>/p2p/node_key.json` holds the node's private key
  (mode `0600`); protect it like any other key file. Deleting it gives the node a
  new node ID.
