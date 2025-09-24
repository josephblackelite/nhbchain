# P2P Networking

The NHB node uses an authenticated TCP transport with a lightweight JSON
protocol. This document describes the wire-level handshake, peer scoring, and
configuration knobs exposed via `config.toml`.

## Handshake

Every inbound and outbound connection begins with a signed handshake payload.
Nodes now include three pieces of network identity that must match exactly:

- **chainId** – the 64-bit identifier derived from the canonical genesis hash.
- **genesisHash** – the raw 32-byte hash of the genesis block.
- **network** – a human-readable network name such as `nhb-local` or
  `nhb-mainnet`.

The payload is signed by the remote validator key over the tuple
`(chainId || genesisHash || network || nonce)` and verified before the peer is
admitted. Connections are rejected when any value differs, preventing accidental
or malicious cross-network gossip.

## Peer scoring & bans

Connections accrue a lightweight reputation score:

- Each well-formed message bumps peer reputation by `+1`.
- Malformed frames (invalid JSON, oversize payloads, etc.) deduct `-2`.
- Repeated malformed traffic is tracked in a one-minute sliding window. When
  at least five messages are observed and 50% or more are invalid, the peer is
  immediately banned for the standard 15-minute cooldown.

Bans are tied to the authenticated node ID so malicious peers cannot evade the
penalty by reconnecting with a fresh TCP session.

## Configuration

`config.toml` now exposes explicit peer lists:

```toml
NetworkName = "nhb-local"
Bootnodes = [
  # Nodes dialed opportunistically at startup for discovery.
]
PersistentPeers = [
  # Validators the node should always maintain connections with.
]
```

Both lists are deduplicated and dialed when the node boots. Use `Bootnodes` for
public discovery endpoints and `PersistentPeers` for partners that should always
be connected.
