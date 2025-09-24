# P2P Networking Hardening (CODEX NET-1)

The NHB node ships with an authenticated TCP transport and JSON framing. The
CODEX NET-1 workstream tightened the peer admission flow, message safety, and
operator controls. This guide documents the current behavior so operators,
integrators, and auditors understand the guarantees exposed by the new stack.

## Startup banner & topology discovery

When the server boots it prints a banner summarizing the chain context and
identity that will be enforced for every peer:

```
NHB P2P listening on 0.0.0.0:3030 | chain=<chainId> | genesis=<hash> | node=<id> | client=<version>
```

Immediately after binding the listen socket, the server begins dialing all
entries from `Bootnodes` and `PersistentPeers`, deduplicated across the two
lists. Any peer flagged as persistent is redialed automatically with
exponential backoff (capped at one minute) whenever the connection drops.

## Authenticated handshake

Every connection performs a mutual JSON handshake before any application
traffic flows. The payload is signed using the node's secp256k1 identity key
and carries the following fields:

| Field | Purpose |
| --- | --- |
| `chainId` | 64-bit network identifier. Must match local configuration. |
| `genesisHash` | Raw 32-byte genesis block hash. Enforces consistent history. |
| `nodeId` | Bech32-encoded address derived from the supplied secp256k1 public key. |
| `clientVersion` | Human-readable client identifier (e.g., `nhbchain/node`). |
| `pubKey` | Compressed secp256k1 public key used for signature verification. |
| `nonce` | 32-byte random challenge to prevent replay. |
| `signature` | secp256k1 signature over `sha256(chainId || genesisHash || clientVersion || nonce)`. |

Handshake validation enforces:

1. Nonce and signature length sanity checks.
2. secp256k1 signature verification and node ID derivation.
3. Exact `chainId` and `genesisHash` equality with the local node.
4. Optional `nodeId` equality if the remote pre-populates it (self-consistency
   check).

Any mismatch or decoding error terminates the connection before the peer is
added to the active set, preventing cross-network gossip or impersonation.
Read and write deadlines ensure the handshake completes within five seconds.

## Token-bucket rate limiting

Each peer is bound to an independent token bucket seeded from
`MaxMsgsPerSecond` with a burst of `2 × MaxMsgsPerSecond`. A global bucket is
sized to `MaxMsgsPerSecond × MaxPeers`, providing a soft fleet-wide cap. When a
peer exhausts its personal budget the server disconnects it and applies a
reputation penalty; when the global bucket empties, the offending message is
rejected and the peer is dropped to protect the network.

Oversized frames are rejected before decoding and count as protocol violations.
Read and write deadlines (default 90s/5s) guard against slowloris behavior and
contribute to the peer's reputation score.

## Reputation, bans, and abuse handling

Every peer maintains a lightweight score influenced by traffic quality:

- Valid messages reward `+1` up to the lifetime of the connection.
- Malformed JSON, oversize frames, or other protocol errors deduct `-2`.
- Rate limit violations deduct `-3`.
- Write timeouts and persistently full outbound queues deduct `-1`.

A sliding one-minute window also tracks the share of invalid messages. If at
least five frames arrive and 50% or more are invalid, the peer is immediately
banned for `PeerBanSeconds` (default 15 minutes) regardless of the current
score.

When a peer's score drops to `-6` or below the node applies the standard ban,
removing the entry from the active set and preventing redial until the cooldown
expires. Bans are keyed off the authenticated node ID derived during the
handshake to prevent easy evasion.

## Configuration reference

The server is configured via `config.toml` (and companion sample files). All
values have sensible defaults but can be tuned per deployment:

| Key | Default | Description |
| --- | --- | --- |
| `MaxPeers` | 64 | Total concurrent peers allowed (inbound + outbound). |
| `MaxInbound` | `MaxPeers` | Cap on inbound peers. Cannot exceed `MaxPeers`. |
| `MaxOutbound` | `MaxPeers` | Cap on outbound dials. Cannot exceed `MaxPeers`. |
| `Bootnodes` | `[]` | Opportunistic peers dialed at startup for discovery. |
| `PersistentPeers` | `[]` | Peers that should always be connected; redialed with backoff. |
| `PeerBanSeconds` | 900s | Ban duration applied after severe misbehavior. |
| `ReadTimeout` | 90s | Maximum silence tolerated while reading from a peer. |
| `WriteTimeout` | 5s | Deadline for sending a message to a peer. |
| `MaxMsgBytes` | 1 MiB | Upper bound on an individual JSON frame size. |
| `MaxMsgsPerSecond` | 32 | Token bucket refill rate applied per peer. |
| `ClientVersion` | `nhbchain/node` | Value advertised during the handshake banner. |

The genesis hash and chain ID are sourced from the node's runtime configuration
and must match across the fleet to form a healthy network.

## Testing & monitoring

`go test ./p2p/...` exercises the handshake, rate limiting, bootnode dialing,
and configuration parsing paths introduced in NET-1. Operators should monitor
logs for `rate limit`, `protocol violation`, and `banned` messages to spot
misbehaving peers in real time. Structured metrics hooks can be layered on top
of the `peerMetrics` counters exposed in the server if deeper observability is
required.
