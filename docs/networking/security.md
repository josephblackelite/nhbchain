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

(`handshakeDigest`, `p2p/handshake.go` line 294). The `nodeId` in the digest is the
sender's own node ID. The signature is a 65-byte secp256k1 signature; the
verifier recovers the public key, derives the node ID from it and compares it to
the claimed `nodeId`. The digest does not include a timestamp or the remote
peer's identity.

## Replay guard

`nonceGuard` (`nonce_guard.go`) remembers `sha256(nodeID + ":" + canonicalNonce)`
for every handshake nonce the server generates or receives. A nonce seen again
within the window is rejected as a replay, and the peer is banned.

- Window: `handshakeReplayWindow` = 10 minutes (`p2p/handshake.go`, line 23), applied
  as the entry lifetime; a sweep runs every minute.
- Capacity: 100,000 entries; the oldest are evicted when full.
- Nonces are canonicalized (Unicode NFKC, invisible format characters removed,
  lower-cased, optional `0x` stripped, hex-decoded) before fingerprinting, so
  differently spelled encodings of one nonce collide.
- The guard is in memory and per process. After an entry expires or the process
  restarts, the same signed frame is accepted again, because nothing else in the
  digest binds it to a time or to the receiving node.
- Metrics: `nhb_p2p_nonce_guard_size`, `nhb_p2p_nonce_guard_evicted_total`.

## Ban reasons and default actions

Ban length is `PeerBanDuration` (`[p2p] BanDurationSeconds`; 3600 in the repo
`config.toml`, and 15 minutes if the value is 0). "Score" refers to the
[reputation](../p2p/reputation.md) score.

| Event | Trigger | Action |
| --- | --- | --- |
| Handshake violation | Chain ID mismatch, genesis mismatch, signature failure, nonce replay | Ban for `PeerBanDuration` (reputation ban plus peerstore `SetBan` and `RecordViolation`), including for persistent peers. |
| Protocol violation | Malformed frame or payload, oversize frame, a payload the node handler reports as invalid | Score -5, peer disconnected; banned if the score crosses `-BanScore`. |
| Invalid message rate | At least 50% invalid messages among at least 5 messages within one minute (`invalidRateThresholdPerc`, `invalidRateSampleSize`, `invalidRateWindow`) | Disconnect and ban. |
| Per-peer or per-IP rate limit | Token bucket empty (see [rate limits](../p2p/ratelimits.md)) | Score -10, disconnect; banned if the score crosses `-BanScore`. Not applied to persistent peers. |
| Global rate cap | Aggregate bucket empty | Disconnect, no penalty. |
| Slow peer | Outbound queue full, or a write error | Score -5. |
| Operator ban | `net_ban` RPC or gRPC `BanPeer` | Ban for the requested seconds (default `PeerBanDuration`); peer disconnected. |

Score-based bans never apply to persistent peers (their score is held at or
below 0 and no ban is set), but handshake violations and operator bans do.

## Log messages

Structured `slog` messages from the P2P server (peer IDs and addresses are
masked in log output):

| Message | Meaning |
| --- | --- |
| `Inbound connection rejected` (attrs `peer_address`, `error`) | Handshake or registration failed for an inbound connection. |
| `Handshake nonce replay from <id> rejected` | Printed to stdout when a replayed nonce is seen. |
| `Protocol violation` / `Protocol violation: invalid message rate` | A message failed validation (`handleProtocolViolation`). |
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
  `X-Forwarded-For` and `X-Real-IP` headers are read only if the caller is
  trusted: `RPCTrustProxyHeaders = true` trusts every caller, otherwise a caller
  must have its address listed in `RPCTrustedProxies`. A request that carries one
  of these headers from an untrusted caller is rejected with HTTP 403
  (`resolveClientIP`). `[RPCProxyHeaders]` sets each header's mode to `single`
  (exactly one address allowed) or `ignore` (a request carrying the header is
  rejected).
- **Rate limits.** `RPCMaxTxPerWindow`, `RPCMaxTxPerIP`, `RPCMaxTxPerIdentity`,
  `RPCMaxTxPerChain` and `RPCMaxTxPerIdentityChain` are request quotas per
  `RPCRateLimitWindow` seconds, tracked per client address, JWT identity and
  caller chain nonce. When a limit is exceeded the server answers HTTP 429 with
  code `-32020` and increments `nhb_rpc_limiter_hits_total{scope,module,route}`.
  Only `RPCMaxTxPerWindow` has its own default, 5 when unset or not positive
  (`config/config.go`, line 619). When unset, `RPCMaxTxPerIP`,
  `RPCMaxTxPerIdentity` and `RPCMaxTxPerChain` take the value of
  `RPCMaxTxPerWindow`, and `RPCMaxTxPerIdentityChain` takes the smaller of the
  identity and chain limits (lines 622-640). The repo `config.toml` sets 120
  per 60 seconds.
- **Timeouts.** `RPCReadHeaderTimeout`, `RPCReadTimeout`, `RPCWriteTimeout` and
  `RPCIdleTimeout` (seconds) are passed to the HTTP server (`cmd/nhb/main.go`,
  lines 570-573); the repo `config.toml` sets them to 0.
- **TLS.** `RPCTLSCertFile` / `RPCTLSKeyFile` enable TLS; `RPCTLSClientCAFile`
  requires client certificates. Without TLS the server needs `RPCAllowInsecure`
  and starts only on a loopback address (an unspecified address needs
  `RPCAllowInsecureUnspecified`), and increments
  `nhb_security_insecure_binds_total{service,loopback}`.
