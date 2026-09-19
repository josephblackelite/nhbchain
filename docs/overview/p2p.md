# P2P networking

The node's P2P layer (`p2p/`) is a TCP transport carrying newline-delimited JSON
frames, with a signed handshake, per-peer and global rate limits, peer
reputation with greylist and ban thresholds, and optional peer exchange (PEX).
It runs inside `cmd/nhb` and, in the split deployment, inside `cmd/p2pd`. See
[services](../architecture/services.md).

## Startup

`Server.Start` binds `ListenAddress` and logs `P2P server listening` with the
listen address, chain id, a genesis hash summary, node id and client version.
It then starts the connection manager and dialers for `Bootnodes`,
`PersistentPeers` (de-duplicated across the two lists) and seeds. Persistent
peers are redialed after a disconnect with exponential backoff starting at
`DialBackoffSeconds` and capped at one minute (`maxDialBackoff`).

## Node identity

The node id is `0x` + `keccak256(uncompressed public key without the 0x04
prefix)`, 32 bytes hex (`deriveNodeIDFromPub`, `p2p/identity.go`).

## Frames and messages

Each frame is one JSON object followed by `\n`. A message is
`{"Type": <byte>, "Payload": <base64 bytes>}` (`p2p.Message` has no JSON tags).
Message types (`p2p/protocol.go`): `0x01` tx, `0x02` block, `0x03` get status,
`0x04` status, `0x05` get blocks, `0x06` blocks, `0x07` proposal, `0x08` vote,
`0x09` ping, `0x0A` pong, `0x0B` handshake, `0x0C` handshake ack, `0x0D` PEX
request, `0x0E` PEX addresses. Frames larger than `MaxMsgBytes` are protocol
violations.

## Handshake

Both sides send a signed JSON handshake and read the other's
(`p2p/handshake.go`), within `HandshakeTimeoutMs`. Fields:

| Field | Meaning |
| --- | --- |
| `protoVersion` | Protocol version; must be `1`. |
| `chainId` | uint64 network id; must equal the local value. |
| `genesisHash` | `0x` hex of the genesis hash; must equal the local value. |
| `nodeId` | Canonical lowercase `0x` hex node id. |
| `nonce` | 12 random bytes, `0x` hex. |
| `clientVersion` | Non-empty string, default `nhbchain/node`. |
| `listenAddrs` | Optional list of dialable `host:port` addresses (unspecified and port-0 addresses are dropped). |
| `sig` | 65-byte secp256k1 signature, `0x` hex. |

The signature is over `keccak256("nhb-handshake-v1" || chainId (8 bytes,
big-endian) || genesisHash || nonce || nodeId string)`. The verifier recovers the
public key from the signature and requires that the node id derived from it
equals the claimed `nodeId`; there is no public key field. A repeated nonce from
the same node id within 10 minutes is rejected as a replay. A chain id, genesis
or signature failure bans the claimed node id for the ban duration
(`markHandshakeViolation`).

## Rate limiting

Every non-persistent peer's messages pass three checks (`Peer.readLoop`):

1. A per-source-IP token bucket (rate `RateMsgsPerSec`, burst `Burst`).
2. A per-peer token bucket with the same rate and burst (a greylisted peer's rate
   is multiplied by 0.25).
3. A global bucket with rate `RateMsgsPerSec x MaxPeers` and burst `Burst x
   MaxPeers`.

Failing the per-IP or per-peer check calls the rate-limit handler: the peer's
reputation drops by 10 and it is disconnected (banned if the score reaches the
ban threshold). Failing the global check disconnects the peer without a score
change. Configured persistent peers are exempt from these buckets.

## Reputation and bans

Scores decay with a 10-minute half-life (`ReputationManager`). Changes:

| Event | Score change |
| --- | --- |
| Valid message | +1 |
| Malformed message or protocol violation | -5 |
| Rate-limit violation | -10 |
| Outbound queue full (64-message queue) | -5 |

A score at or below `-GreyScore` (default 50) greylists the peer for a minute; at
or below `-BanScore` (default 100) it bans the peer for `BanDurationSeconds`
(persistent peers are never banned by score). In addition, if at least 5 messages
in a one-minute window have arrived and at least 50% are invalid, the peer is
dropped for invalid message rate. Ping messages are sent to each peer every
`PingIntervalSeconds` (default 30). A peer that sends nothing within `ReadTimeout`
is disconnected by the socket read deadline. `PingTimeoutSeconds` is accepted and
stored by the server but nothing in `p2p/` reads it.

## Configuration

Keys in `config.toml` (`config/config.go`). The top-level keys and the `[p2p]`
table are merged at load: whichever side is unset takes the other's value.
Defaults are those applied by `config.Load` when a key is `0`/empty.

| Key | Default | Meaning |
| --- | --- | --- |
| `ListenAddress` | `:6001` | TCP listen address. |
| `MaxPeers` | 64 | Total peers. |
| `MaxInbound`, `MaxOutbound` | `MaxPeers` | Caps; cannot exceed `MaxPeers`. |
| `MinPeers` | `MaxPeers / 2` | Target minimum. |
| `OutboundPeers` | `MaxOutbound` | Outbound connection target. |
| `Bootnodes`, `PersistentPeers` | `[]` | Peers to dial. |
| `PeerBanSeconds` (`[p2p] BanDurationSeconds`) | 3600 | Ban duration. |
| `ReadTimeout` / `WriteTimeout` | 90 s / 5 s | Socket deadlines. |
| `MaxMsgBytes` | 1048576 | Maximum frame size. |
| `MaxMsgsPerSecond` (`[p2p] RateMsgsPerSec`) | 32 | Per-peer token refill rate. |
| `ClientVersion` | `nhbchain/node` | Sent in the handshake. |
| `[p2p] NetworkId` | 430060579445266314 | Parsed from the config (decimal string or integer) but not used by the node: the handshake `chainId` is the node's chain id (`Node.ChainID`). |
| `[p2p] ExternalAddress` | empty | This node's publicly dialable `host:port`, advertised in the handshake when `ListenAddress` is unspecified. |
| `[p2p] Seeds` | `[]` | Seed entries. |
| `[p2p] Burst` | 200 | Token bucket burst. |
| `[p2p] GreyScore`, `BanScore` | 50, 100 | Reputation thresholds. |
| `[p2p] HandshakeTimeoutMs` | 5000 | Handshake deadline. |
| `[p2p] PingIntervalSeconds`, `PingTimeoutSeconds` | server defaults 30 s, 120 s when unset | Ping interval; the timeout value is not used (see above). |
| `[p2p] DialBackoffSeconds` | 30 | Initial redial backoff. |
| `[p2p] PEX` | true | Enable peer exchange. |

PEX (`p2p/pex.go`) exchanges up to 32 recently seen addresses per response and
keeps address entries for 60 minutes.

## Tests

`go test ./p2p/...` and `go test ./config` cover the handshake, rate limiting,
reputation and configuration parsing.
