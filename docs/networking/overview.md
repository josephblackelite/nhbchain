# Networking Overview

This page describes the peer-to-peer layer in `p2p/`: node identity, the wire
format, the handshake, peer discovery, the peerstore, and the connection
manager. It is written from the code; file references are to `p2p/` unless
stated otherwise. Related pages:

- [Security notes](security.md): handshake digest, replay guard, bans.
- [Peer exchange](pex.md), [seeds](seeds.md), [operations](ops.md),
  [network RPC](net-rpc.md), [observability](observability.md).
- [p2p rate limits](../p2p/ratelimits.md), [reputation](../p2p/reputation.md),
  [p2pd service](../p2p/service.md).

The transport is **plain TCP** carrying newline-delimited JSON. The P2P code has
no TLS, QUIC or UDP (`server.go`: `net.Listen("tcp", ...)` and
`net.Dialer.DialContext(ctx, "tcp", ...)`). Node identity is proven by a
signature inside the handshake, not by the transport.

## Identity and node ID

Each node has a persistent secp256k1 key. `p2p.LoadOrCreateIdentity(path)`
(`identity.go`) reads it or creates it. `cmd/nhb` and `cmd/p2pd` use
`<DataDir>/p2p/node_key.json`, a JSON file `{"privateKey": "<hex>"}` written with
mode `0600` (a raw-hex file is also accepted when reading). The node ID is

```
nodeID = "0x" + hex(keccak256(uncompressedPublicKey[1:]))   // lower-case
```

This key is separate from the validator's consensus key and is not tied to any
funded wallet or account.

## Wire format

After the handshake, every message is one JSON object followed by `\n`:

```json
{"Type": 9, "Payload": "<base64 of the type's JSON payload>"}
```

(`Message{Type byte; Payload []byte}` in `interface.go`; `peer.go` `writeMessage`
and `readLoop`.) Frames larger than `MaxMsgBytes` (default 1 MiB) are a protocol
violation. Message types (`protocol.go`):

| Type | Name | Payload |
| --- | --- | --- |
| `0x01` | Tx | transaction JSON |
| `0x02` | Block | block JSON |
| `0x03` / `0x04` | GetStatus / Status | `{}` / `{"Height": n}` |
| `0x05` / `0x06` | GetBlocks / Blocks | `{"From": n}` / `{"Blocks": [...]}` |
| `0x07` | Proposal | BFT signed proposal |
| `0x08` | Vote | BFT signed vote |
| `0x09` / `0x0A` | Ping / Pong | `{"nonce": n, "timestamp": unixNano}` |
| `0x0B` / `0x0C` | Handshake / HandshakeAck | reserved; received frames of these types are accepted and ignored |
| `0x0D` / `0x0E` | PexRequest / PexAddresses | see [pex.md](pex.md) |

Ping and Pong are answered inside the peer; everything else that is not a PEX
message goes to the node's message handler.

## Handshake (protocol version 1)

Right after the TCP connection is up, **both sides send one frame and then read
one frame** (`handshake.go`, `performHandshake`). The frame is a JSON object
followed by `\n` (this initial frame is not wrapped in the `Message` envelope):

| Field | Meaning |
| --- | --- |
| `protoVersion` | `1`. Any other value is rejected. |
| `chainId` | uint64. Must equal the local chain ID, which is the first 8 bytes of the genesis hash read big-endian (`core/blockchain.go`). |
| `genesisHash` | `0x` hex of the genesis hash. Must equal the local one. |
| `nodeId` | Sender's node ID, canonical lower-case `0x` hex. |
| `nonce` | 12 random bytes, `0x` hex. |
| `clientVersion` | Non-empty free-form string (config `ClientVersion`, default `nhbchain/node`). |
| `listenAddrs` | Optional list of the sender's dialable `host:port` addresses (its `ListenAddress` and `ExternalAddress`; unspecified hosts and port `0` are dropped). The receiver looks at the first 64 entries and keeps at most 8 (`sanitizeListenAddrs`). |
| `sig` | 65-byte secp256k1 signature, `0x` hex. |

The signed digest is

```
keccak256( "nhb-handshake-v1"
           || chainId as 8 bytes big-endian
           || genesis hash (raw bytes)
           || nonce (raw 12 bytes)
           || the ASCII bytes of the canonical "0x..." nodeId string )
```

(`handshakeDigest`). The verifier recovers the public key from `sig`, derives the
node ID from it, and requires it to equal the claimed `nodeId`.

Checks run in this order (`verifyHandshake`): protocol version; non-empty
`clientVersion`; non-empty canonical `nodeId`; canonical 12-byte nonce; chain ID;
genesis hash; signature length and recovery; nonce replay. A chain ID or genesis
mismatch bans the node it names only when the packet's signature really comes
from that node, and never when that node is a configured persistent peer. A
signature that does not match the node ID and a replayed nonce are refused and
nothing is held against the node ID (see [security](security.md)). Then
`initPeer` rejects a connection to itself, a peer that is currently banned, and a
node ID that is already connected, and registers the peer subject to `MaxPeers`,
`MaxInbound`, `MaxOutbound` and a limit of 8 established inbound connections per
remote address (configured peers, loopback and non-IP hosts excepted). A peer is
recorded in the peer records, the PEX book and the peerstore only after
`registerPeer` has accepted it.

Before the handshake starts, `admitInbound` applies per-address and global
limits to a new inbound connection; see [security](security.md#inbound-admission).

The whole exchange must finish within `HandshakeTimeout` (`HandshakeTimeoutMs`,
3000 in the repo `config.toml`; the server's own default is 5 s). The outcome is
counted in `nhb_p2p_handshakes_total{result="success"|"failure"}`.

Persistent-peer status is granted by node ID only. The `listenAddrs` a peer
reports are unsigned, so matching them against the configured bootnode and
persistent-peer list does not confer trust (`isPersistentRemote`).

## Peer lifecycle

1. **Dial or accept.** Outbound dials come from the sources below; inbound
   connections come from the listener.
2. **Handshake** as above.
3. **Connected.** The peer starts three goroutines: a read loop, a write loop and
   a keepalive loop that sends a Ping every `PingIntervalSeconds` (30 in
   `config.toml`). The read loop sets a read deadline of `ReadTimeout` (90 s by
   default) before every frame, so a peer that sends nothing for that long is
   disconnected.
4. **Disconnect.** Any protocol violation, rate-limit hit, write error, or
   connection-manager prune terminates the peer. Terminating with `ban = true`
   applies a ban score. A persistent outbound peer is re-dialed with exponential
   backoff.

## Discovery

Outbound targets come from:

1. `[p2p] Bootnodes` and `PersistentPeers`, dialed at start
   (`startDialers`, `connmanager.go`). Both lists are treated as persistent
   addresses. A failed dial is retried with a delay that starts at
   `DialBackoffSeconds` (30 in `config.toml`) and doubles up to one minute.
2. **Seeds**: `[p2p] Seeds` entries (`0xNODEID@host:port`) plus entries from the
   on-chain `network.seeds` registry; see [seeds.md](seeds.md). The connection
   manager runs one dial loop per active seed.
3. **The peerstore**: previously seen peers with their addresses.
4. **Handshake addresses.** Each handshake that ends in a registered peer records
   the peer's `listenAddrs` (or, if it sent none, the address it was dialed at, or
   the connection's remote address) in the PEX address book and the peerstore.

The node answers `PexRequest` messages from peers (see [pex.md](pex.md)), but the
node's own code never sends a `PexRequest`: `NewPexRequestMessage` has no caller
outside tests. Because a `PexAddresses` frame is accepted only as the reply to a
request this node sent, address discovery relies on handshakes, seeds and the
peerstore.

`p2p/integration/mesh_test.go` (`TestMiniMeshIntegration`) exercises a small mesh
of nodes plus a node on a different chain ID that is rejected in the handshake:

```bash
go test ./p2p/integration -run TestMiniMeshIntegration -count=1
```

## Peerstore

`p2p.NewPeerstore(path, 0, 0)` (`peerstore.go`) is a LevelDB database at
`<DataDir>/p2p/peerstore`, with an in-memory index by node ID and by address.
Each record (key `peer:<nodeID>`, JSON value) has:

| Field | Meaning |
| --- | --- |
| `addr`, `nodeID` | Last known address and the node ID. One record per node ID; a new address replaces the old one. |
| `score` | Float in `[-100, 1000]`. `RecordSuccess` adds 1; `RecordFail` halves a positive score; `RecordViolation` subtracts 10. |
| `lastSeen` | Time of the last success, failure or violation. |
| `fails` | Consecutive failed dials; reset to 0 on success. |
| `bannedUntil` | Ban expiry. |
| `violations`, `lastViolation` | Handshake violation counters. |

The store is bounded: at most 2,048 records (`defaultPeerstoreMaxEntries`), a
record no larger than 4,096 bytes (address at most 255 characters, node ID at most
128), and records not seen for 14 days are pruned (`defaultPeerstoreTTL`; the
connection manager prunes once an hour, and loading the store at start drops
expired, oversized and undecodable records). When it is full, the least recently
seen record that is not connected and not banned is evicted first; peers that are
connected right now are protected from eviction and pruning. In addition the
server keeps at most 4,096 in-memory peer records.

Dial scheduling (`NextDialAt`): a banned peer waits until `bannedUntil`; with no
failures the next dial is now; otherwise the peer waits
`baseBackoff * 2^(fails-1)` after `lastSeen`, where `baseBackoff` is 1 s and the
cap is 30 minutes.

## Connection manager

`connManager` (`connmanager.go`) runs a check every 3 seconds:

- **Fill.** It computes `needed = max(OutboundPeers - outbound, MinPeers - total)`,
  limited to the free slots (`MaxPeers - total`), and dials up to that many
  candidates. Candidates are peerstore entries sorted by score (highest first)
  then most recent `lastSeen`, skipping connected peers, banned peers (runtime
  reputation or peerstore) and peers whose backoff has not elapsed; active seeds
  are appended only if fewer candidates were found than requested.
- **Prune.** If more than `MaxPeers` peers are connected, it disconnects the
  worst until the count fits. Persistent peers are never chosen. The victim is the
  peer with, in order: more misbehavior incidents, a lower score, fewer useful
  messages, a higher ping latency, an older `lastSeen`; on a tie, inbound before
  outbound.

`MaxPeers`, `MaxInbound`, `MaxOutbound` are also enforced when a peer registers.
Values in the repo `config.toml` `[p2p]` section: `MaxPeers = 64`,
`MaxInbound = 60`, `MaxOutbound = 30`, `MinPeers = 12`, `OutboundPeers = 16`.
When a value is unset, `MaxPeers` defaults to 64, `MaxInbound` and `MaxOutbound`
to `MaxPeers`, `MinPeers` to half of `MaxPeers`, and `OutboundPeers` to
`MaxOutbound` (`config/config.go` `Load`, `p2p/server.go` `NewServer`).

## NAT

At start the connection manager logs whether `ListenAddress` is public, private
or unspecified. For a private or unspecified address it logs that UPnP mapping is
not supported and that the operator must forward the TCP port by hand
(`logNATStatus`, `logUPnPStub`). No UPnP or NAT-PMP negotiation exists in the
code. Set `[p2p] ExternalAddress` (`host:port`) so peers that reach you inbound
learn a dialable address from your handshake.
