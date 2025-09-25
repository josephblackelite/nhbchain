# Networking Overview

This document captures the foundational pieces of the NHB peer-to-peer
subsystem introduced in NET-2A.

## Identity & NodeID

Each node maintains a persistent secp256k1 identity stored on disk. By default
`cmd/nhb` writes the key to `<DataDir>/p2p/node_key.json`, creating the directory
on first run. The public component of this key is hashed with Keccak-256 to
produce the node's canonical identifier:

```
nodeID = keccak256(uncompressedPubKey[1:]) // 0x-prefixed, lower-case hex
```

Loading (or generating) an identity is handled by `p2p.LoadOrCreateIdentity`. The
helper returns both the private key and derived `NodeID`:

```go
identityPath := filepath.Join(cfg.DataDir, "p2p", "node_key.json")
identity, err := p2p.LoadOrCreateIdentity(identityPath)
if err != nil {
        log.Fatalf("load node identity: %v", err)
}
log.Printf("nodeId=%s", identity.NodeID)
```

Persisting the identity allows subsequent restarts to present a stable node ID
and signature key without manual key management.

## Handshake v1

The handshake is an authenticated JSON frame exchanged immediately after a TCP
connection is established. Both peers transmit the following payload:

| Field | Description |
| ----- | ----------- |
| `protoVersion` | Static protocol discriminator (`1`). |
| `chainId` | Target chain identifier the node belongs to. |
| `genesisHash` | Hex-encoded canonical genesis hash (32 bytes). |
| `nodeId` | Sender's 0x-prefixed NodeID derived from its identity key. |
| `nonce` | 32-byte random challenge encoded as hex. |
| `clientVersion` | Free-form software/version string exposed via RPC. |
| `sig` | 65-byte ECDSA signature covering the handshake digest. |

The signature is produced with the sender's node identity over the digest
outlined in [the security notes](./security.md). Peers reply with a
`HANDSHAKE_ACK` message type once the frame validates, although the payload is
the same as the initial `HANDSHAKE` frame.

A minimal illustration of constructing the outbound message is shown below. It
mirrors the logic in `p2p/handshake.go` and can be used for integration tests or
other tooling:

```go
nonce := make([]byte, 32)
if _, err := rand.Read(nonce); err != nil {
        log.Fatal(err)
}
msg := struct {
        Proto uint32 `json:"protoVersion"`
        Chain uint64 `json:"chainId"`
        Genesis string `json:"genesisHash"`
        NodeID string `json:"nodeId"`
        Nonce  string `json:"nonce"`
        Client string `json:"clientVersion"`
        Sig    string `json:"sig"`
}{
        Proto: 1,
        Chain: cfg.ChainID,
        Genesis: hex.EncodeToString(genesisBytes),
        NodeID: identity.NodeID,
        Nonce:  hex.EncodeToString(nonce),
        Client: cfg.ClientVersion,
}
digestInput := bytes.Join([][]byte{
        uint64ToBytes(msg.Chain),
        genesisBytes,
        nonce,
        mustDecodeHex(msg.NodeID),
}, nil)
digest := crypto.Keccak256(digestInput)
signature, err := ethcrypto.Sign(digest, identity.PrivateKey.PrivateKey)
if err != nil {
        log.Fatal(err)
}
msg.Sig = hex.EncodeToString(signature)
```

*Helper functions such as `uint64ToBytes` simply encode the integer into an
8-byte big-endian buffer; the production code uses the same representation.*

### Flow

The handshake flow is intentionally symmetric and short:

1. **Dialing** – initiate or accept a TCP connection.
2. **Handshaking** – exchange the JSON frames above and validate the digest,
   chain/genesis compatibility, and nonce replay window. A `HANDSHAKE_ACK`
   message signals success once verification completes.
3. **Connected** – both peers register the connection, start the read/write
   loops, and enable keepalive pings.

Failures at any stage immediately close the socket and increment the peer's
reputation penalties. Handshake success snapshots the peer metadata for RPC
exposure.

## State Machine

At a high level the peer lifecycle is:

```
Outbound Dial Loop
        |
        v
Dialing  -->  Handshaking  -->  Connected
   ^            |               |
   |            v               v
Reconnect   Reject/Ban      Keepalive
```

* **Outbound Dial Loop** – iterates through configured seeds and peerstore
  entries, scheduling eligible outbound dials while respecting backoff and ban
  windows.
* **Dialing** – initiated either manually (`Connect`) or by the connection
  manager.
* **Handshaking** – performs the authenticated handshake exchange and enforces
  policy (chain, genesis, signature, nonce replay).
* **Connected** – schedules read/write loops, activates PING/PONG keepalive, and
  feeds traffic into the application handler.

Peers that violate protocol expectations during any phase are disconnected and
optionally banned according to the configured reputation policy.

## Discovery Lifecycle

Peer discovery progresses through a short pipeline before settling into the
steady gossip mesh:

```
Seed Bootstrapping --> Authenticated Handshake --> PEX Gossip --> Steady-State Mesh
        ^                                                      |
        |------------------------------------------------------|
```

1. **Seed Bootstrapping** – the node dials the configured seed list and any
   persisted peers from the peerstore, establishing an initial foothold.
2. **Authenticated Handshake** – successful handshakes elevate peers into the
   active set and record their addresses with fresh timestamps.
3. **PEX Gossip** – connected peers exchange `PEX_REQUEST`/`PEX_ADDRESSES`
   messages to learn about additional endpoints while enforcing the address
   TTL window and deduplication rules.
4. **Steady-State Mesh** – the connection manager maintains target counts by
   recycling aged peers, periodically requesting PEX samples to replenish the
   dial queue as nodes churn.

## Peerstore

The NET-2B release introduces a durable peerstore backed by LevelDB. Every
successful handshake writes an entry that survives restarts, ensuring dial
scheduling, bans, and scores persist across crashes or maintenance reboots.

### Stored Fields

| Field | Description |
| ----- | ----------- |
| `addr` | Last observed multiaddr/TCP endpoint for the peer. |
| `nodeID` | Canonical NodeID derived from the identity key. |
| `score` | Rolling health score; successes increment, failures decay. |
| `lastSeen` | Timestamp of the most recent dial outcome (success or fail). |
| `fails` | Consecutive failure counter driving exponential backoff. |
| `bannedUntil` | Wall-clock time after which the peer may reconnect. |

Entries are deduplicated by `nodeID`. When a peer re-announces itself with a new
address the previous mapping is replaced, preventing stale endpoints from
lingering. LevelDB handles on-disk compaction automatically; no proactive
eviction policy is required, but operators can trigger `compactdb` if the store
grows unusually large.

### Dial Backoff

Dial attempts follow an exponential backoff controlled by the failure counter.

```text
# Exponential dial backoff
function nextDial(lastSeen, fails):
    if fails <= 0:
        return now
    delay = baseBackoff * 2^(fails-1)
    delay = min(delay, maxBackoff)
    return lastSeen + delay
```

For example, with `baseBackoff = 1s` and `maxBackoff = 30m`, a peer that fails
three consecutive dials will be retried after 4 seconds, then 8 seconds, then
16 seconds. A successful dial resets both the failure counter and backoff delay
to zero.

## Connection Manager Policy

NET-2E expands the connection manager into an active curator of the peer set.
The controller maintains separate targets for **total** connections and
**outbound** dials while respecting the configured hard maximum.

* **MinPeers** – the minimum healthy peer count the node aims to keep. When the
  total number of connected peers drops below this floor the manager immediately
  schedules new dials.
* **OutboundPeers** – the desired number of outbound sessions. If churn leaves
  the node with too few outbound links (even if `MinPeers` is satisfied) the
  manager replenishes the shortfall.
* **MaxPeers** – an absolute ceiling enforced during registration. The manager
  never attempts to exceed this value.

Target selection is score-aware. The dial queue is populated by taking a
snapshot of the peerstore, discarding banned entries, and then sorting by:

1. Highest reputation score (`PeerstoreEntry.Score`).
2. Most recent `lastSeen` timestamp.

Seed entries are only considered when the peerstore cannot satisfy demand.
Before scheduling a dial the manager checks:

* The peer is not already connected nor pending.
* The peer is not banned by either the peerstore or the runtime reputation
  engine.
* The exponential backoff window (`NextDialAt`) has elapsed.

### Pruning Rules

When churn or manual overrides push the active set above `MaxPeers`, the manager
selects a victim to disconnect. Candidates that are flagged as persistent are
never pruned. Among the remaining peers the selection algorithm chooses:

1. The lowest reputation score.
2. If scores tie, the stalest `lastSeen` timestamp.
3. If the tie persists, inbound peers are preferred for pruning ahead of
   outbound peers.

Pruned peers are disconnected with a log entry indicating the score and last
contact time. Persistent peers (bootnodes, static peers) remain untouched so
operators retain deterministic connectivity anchors.

### Recommended Targets

| Hardware Tier | MinPeers | OutboundPeers | MaxPeers |
| ------------- | -------- | ------------- | -------- |
| Light (1 vCPU / 2 GiB) | 8  | 6  | 16 |
| Standard (2 vCPU / 4 GiB) | 16 | 12 | 32 |
| Validator (4+ vCPU / 8+ GiB) | 24 | 18 | 48 |

These values balance CPU, memory, and bandwidth trade-offs. Operators can raise
`MaxPeers` or `MinPeers` further on high-end hardware, but the defaults ensure a
robust gossip mesh without overwhelming smaller instances.
