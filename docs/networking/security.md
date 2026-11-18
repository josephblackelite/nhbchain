# Networking Security Notes

What the P2P layer checks, bans and logs, taken from `p2p/`. For the handshake
frame itself see the [overview](overview.md). Broader deployment guidance is in
[Network security playbook](../security/networking.md) and
[Network hardening playbook](../security/network-hardening.md).

## Signed challenge

Each side signs

```
digest = keccak256( "nhb-handshake-v1"
                    || chainID (8 bytes, big-endian)
                    || genesisHash (raw bytes)
                    || nonce (12 raw bytes)
                    || ASCII bytes of the sender's canonical "0x..." nodeId )
```

(`handshakeDigest`, `p2p/handshake.go`). The `nodeId` in the digest is the
sender's own node ID. The signature is a 65-byte secp256k1 signature; the
verifier recovers the public key, derives the node ID from it and compares it to
the claimed `nodeId`. The digest does not include a timestamp or the remote
peer's identity.

## Replay guard

`nonceGuard` (`nonce_guard.go`) remembers `sha256(nodeID + ":" + canonicalNonce)`
for every handshake nonce the server generates or receives. A nonce seen again
within the window is rejected as a replay. The node the packet names is not
banned for it: a handshake is not a challenge, so whoever has seen one can send
it again, and a ban would let anybody lock that node out of its peers.

- Window: `handshakeReplayWindow` = 10 minutes (`p2p/handshake.go`), applied
  as the entry lifetime; a sweep runs every minute.
- Capacity: 100,000 entries; the oldest are evicted when full.
- Nonces are canonicalized (Unicode NFKC, invisible format characters removed,
  lower-cased, optional `0x` stripped, hex-decoded) before fingerprinting, so
  differently spelled encodings of one nonce collide.
- The guard is in memory and per process. After an entry expires or the process
  restarts, the same signed frame is accepted again, because nothing else in the
  digest binds it to a time or to the receiving node.
- Metrics: `nhb_p2p_nonce_guard_size`, `nhb_p2p_nonce_guard_evicted_total`.

## Challenge-response extension

The hello above is a bearer token: it is signed before anything is read from the other side, so a
recording of it can be shown again to impersonate the node ID it names. A node that has the
extension (`handshakeExtVersion` = 2) adds an optional `ext` block to its hello, `{"v": 2, "n":
<fresh 32 bytes>}`. The block is outside the signed v1 digest, which is unchanged, so a node that
does not know the extension ignores the field and verifies the hello as before.

When both hellos carry the block, each side then sends one more frame, `{"hs2auth": <signature>}`,
signed by the key behind its node ID over `keccak256("nhb-handshake-auth-v2" | chain id | genesis |
role | signer node ID | peer node ID | signer challenge | peer challenge | sha256(listen addresses)
| sha256(client version))`. The role is the side the signer is on (dialer or listener). A recorded
hello cannot be answered, because the proof has to cover the challenge the other node chose for this
connection; a proof made on one connection is of no use on another; a proof made as one role is
refused by a node that expects the other; and the listen addresses and client version, which the v1
signature does not cover, can no longer be changed on the way. The stream is still plaintext after
the handshake, so this closes impersonation by replay, not an attacker who relays a whole
connection.

`[p2p] HandshakeChallenge` sets how a node treats it: `auto` (the default) offers the block and
proves its key to a peer that offers it too, and handles any other peer as a v1 peer, sending it
nothing it does not expect; `off` behaves exactly like a v1 node; `require` refuses a peer that
does not offer it. In `auto` a link observer can still strip the block from a recorded hello and
have it taken for a v1 hello, so the replay is closed only between nodes that all run `require`,
which is the setting to move to once every peer has upgraded.

## Ban reasons and default actions

Ban length is `PeerBanDuration` (`[p2p] BanDurationSeconds`; 3600 in the repo
`config.toml`, and 15 minutes if the value is 0). "Score" refers to the
[reputation](../p2p/reputation.md) score.

| Event | Trigger | Action |
| --- | --- | --- |
| Handshake violation | Chain ID or genesis mismatch in a handshake that is signed by the node it names | Ban for `PeerBanDuration` (reputation ban plus peerstore `SetBan` and `RecordViolation`). Never applied to a configured persistent peer. |
| Handshake refused | Signature that does not match the claimed node ID, a replayed nonce, a proof (`hs2auth`) that does not verify, or no offer of the challenge extension when `HandshakeChallenge = "require"` | The connection is closed. Nothing is held against the node ID in the packet. |
| Protocol violation | Malformed frame or payload, oversize frame, an unsolicited or oversized `PexAddresses`, a payload the node handler reports as invalid (a transaction that fails validation, or a block that the node rejects for its content when it syncs from a peer; a failure of the node's own storage is not one) | Score -5, peer disconnected; banned if the score crosses `-BanScore`. |
| Invalid message rate | At least 50% invalid messages among at least 5 messages within one minute (`invalidRateThresholdPerc`, `invalidRateSampleSize`, `invalidRateWindow`) | Disconnect and ban. |
| Per-peer or per-IP rate limit | Token bucket empty (see [rate limits](../p2p/ratelimits.md)) | Score -10, disconnect; banned if the score crosses `-BanScore`. Not applied to persistent peers. |
| Global rate cap | Aggregate bucket empty | Disconnect, no penalty. |
| Write error | A write fails or times out | Score -5, peer disconnected. |
| Send queue full | A peer's outbound queue has no room for a message | The message is dropped for that peer; the connection is kept and the score is unchanged. |
| Chain-data request over budget | `GetBlocks` or `GetStatus` above the per-address or shared budget | The request is dropped; no score change, no disconnect. |
| Operator ban | `net_ban` RPC or gRPC `BanPeer` | Ban for the requested seconds (default `PeerBanDuration`); peer disconnected. |

Score-based bans never apply to persistent peers (their score is held at or
below 0 and no ban is set), and neither do handshake bans; operator bans do.

Banned records are kept longer than the others when the peerstore (2,048
records) or the reputation table (4,096 records) is full, but only while they
make up at most a quarter of the table. Above that, the least recently seen
banned record is dropped first, so that a large number of bans cannot push out
the records of the peers that are not banned, and a ban can be forgotten before
it expires.

## Inbound admission

`admitInbound` (`p2p/server.go`) runs before anything is read from a new inbound
connection. Per remote address it allows a burst of 10 new connections and then
one every 6 seconds, at most 4 handshakes in flight and, at registration, at most
8 established inbound connections; across all addresses at most 64 handshakes
may be in flight. Connections from loopback, from a host that is not an IP
address and from the address of a configured bootnode or persistent peer are
exempt from the per-address limits, and are never refused a handshake slot on
account of others. The per-address limits (including the request and message
budgets) count an IPv4 address on its own and an IPv6 address as its /64 network.
A refused connection is closed, and a warning
`Inbound connection refused: too many connections or handshakes` is logged at
most once a second. Nothing is recorded about a peer before it has been
registered, except the ban for a signed chain ID or genesis mismatch (see
[Ban reasons](#ban-reasons-and-default-actions)).

## Requests for chain data

A `GetBlocks` or `GetStatus` request costs the node that answers it reads and
bandwidth. Each remote address has a budget for each (`GetBlocks`: a burst of 16
and 4 a second; `GetStatus`: a burst of 8 and 2 a second), and one more budget is
shared by all addresses (`GetBlocks`: a burst of 64 and 32 a second; `GetStatus`:
a burst of 128 and 64 a second). A request over either budget is dropped and the
peer is not disconnected or blamed for it, because an honest node can be led into
sending many. Configured persistent peers are not limited. The answer to a
request goes to the peer that asked, not to every peer
(`admitRequest`, `dispatch`, `PeerMessageHandler`).

A node that is told a peer is ahead of it, by a status report or by a block that
does not follow its own chain, asks its peers for blocks from the height it
needs. However many such messages arrive, it asks for the blocks from the same
height at most once a second, and it asks for the next batch as soon as it has
applied the last one (`core/node.go`, `handleNetworkStatus`, `handleNetworkBlocks`).
One answer holds at most 128 blocks or 512 KiB of encoded blocks.

## Log messages

Structured `slog` messages from the P2P server (peer IDs and addresses are
masked in log output):

| Message | Meaning |
| --- | --- |
| `Inbound connection rejected` (attrs `peer_address`, `error`) | Handshake or registration failed for an inbound connection (logged at most once a second). |
| `Inbound connection refused: too many connections or handshakes` | `admitInbound` refused a connection before the handshake. |
| `Dropping chain data requests from a peer over its budget` | `admitRequest` dropped a `GetBlocks` or `GetStatus` request; the peer was not blamed. |
| `Peer send queue full; dropping messages for this peer` | A peer's outbound queue is full; messages are dropped for it and the connection is kept. |
| `Handshake nonce replay from <id> rejected` | Printed to stdout when a replayed nonce is seen; the node is not banned. |
| `Protocol violation` / `Protocol violation: invalid message rate` | A message failed validation (`handleProtocolViolation`). |
| `Pruned peerstore records while loading` | The peerstore dropped expired, oversized or undecodable records at start. |
| `Peer exceeded rate limit` | Per-peer or per-IP bucket empty. |
| `Global rate cap exceeded` | Global bucket empty. |
| `Ignoring generic rate-limit disconnect for configured persistent peer` | A rate limit tripped for a persistent peer and was ignored. |
| `Peer disconnected and banned` | A peer left with `ban = true`. |
| `Pruning peer due to connection limits` | Connection manager pruned a peer above `MaxPeers`. |
| `Peer connected`, `Peer disconnected` | Lifecycle. |

## RPC perimeter

The JSON-RPC server's proxy and rate-limit settings are separate from the P2P
layer (`rpc/http.go`, keys in `config/config.go`):

- **Client address.** By default the client address is the TCP peer address. The
  `X-Forwarded-For` and `X-Real-IP` headers are read only if the caller's address
  is listed in `RPCTrustedProxies`. `RPCTrustProxyHeaders = true` does not widen
  that: with an empty `RPCTrustedProxies` it trusts nobody (the server logs a
  warning at start), so list the proxy's address (`127.0.0.1` for a proxy on the
  same host). A request that carries one of these headers from a caller that is
  not listed is rejected with HTTP 403 (`resolveClient`). A forwarded value that
  is not an IP address is refused the same way, at most 5 addresses are allowed in
  `X-Forwarded-For`, and the first one is the client. `[RPCProxyHeaders]` sets each
  header's mode to `single` (exactly one address allowed) or `ignore` (a request
  carrying the header is rejected). Rate limits count an IPv6 client as its /64.
- **Rate limits.** `RPCMaxTxPerWindow`, `RPCMaxTxPerIP`, `RPCMaxTxPerIdentity`,
  `RPCMaxTxPerChain` and `RPCMaxTxPerIdentityChain` are request quotas per
  `RPCRateLimitWindow` seconds, tracked per client address, JWT identity and
  caller chain nonce. When a limit is exceeded the server answers HTTP 429 with
  code `-32020` and increments `nhb_rpc_limiter_hits_total{scope,module,route}`.
  Only `RPCMaxTxPerWindow` has its own default, 5 when unset or not positive
  (`config/config.go`, `Load`). When unset, `RPCMaxTxPerIP`,
  `RPCMaxTxPerIdentity` and `RPCMaxTxPerChain` take the value of
  `RPCMaxTxPerWindow`, and `RPCMaxTxPerIdentityChain` takes the smaller of the
  identity and chain limits. The repo `config.toml` sets 120 per 60 seconds.
- **Expensive public reads.** The public read methods that scan blocks or hold the
  state lock run in a small admission pool (`RPCQuery*` keys, and
  `RPCDisableExplorerLoop`). A refused query gets HTTP 429, code `-32020` and a
  `Retry-After` header; one that runs out of time gets HTTP 503, code `-32021`. See
  [Public RPC query limits](../ops/rpc-query-limits.md).
- **Timeouts.** `RPCReadHeaderTimeout`, `RPCReadTimeout`, `RPCWriteTimeout` and
  `RPCIdleTimeout` (seconds) are passed to the HTTP server (`cmd/nhb/main.go`);
  the repo `config.toml` sets them to 0.
- **TLS.** `RPCTLSCertFile` / `RPCTLSKeyFile` enable TLS; `RPCTLSClientCAFile`
  requires client certificates. Without TLS the server needs `RPCAllowInsecure`
  and starts only on a loopback address (an unspecified address needs
  `RPCAllowInsecureUnspecified`), and increments
  `nhb_security_insecure_binds_total{service,loopback}`.
