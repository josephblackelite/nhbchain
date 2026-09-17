# Networking Security Notes

The NET-2A handshake introduces a signed challenge to prevent spoofing and
replay attacks while keeping the exchange lightweight.

## Signed challenge

For each handshake a node signs the digest:

```
digest = keccak256(
    bigEndian(chainID) || genesisHash || nonce || remoteNodeID,
)
```

* `chainID` is encoded as an 8-byte big-endian integer.
* `genesisHash` is the raw 32-byte hash of the canonical genesis block.
* `nonce` is a freshly generated 32-byte random value unique to this handshake.
* `remoteNodeID` is the sender's advertised NodeID (from the perspective of the
  verifying peer).

Including the remote NodeID binds the signature to the identity being claimed,
so any tampering with `nodeId` or swapping in a different identity invalidates
`SigToPub` recovery. The random nonce ensures the signed material is unique per
handshake, preventing replay even if the other fields remain constant.

A direct translation of the digest computation is shown below:

```go
func handshakeDigest(chainID uint64, genesis, nonce []byte, nodeID string) ([]byte, error) {
        var buf [8]byte
        binary.BigEndian.PutUint64(buf[:], chainID)
        idBytes, err := hex.DecodeString(strings.TrimPrefix(nodeID, "0x"))
        if err != nil {
                return nil, err
        }
        payload := bytes.Join([][]byte{buf[:], genesis, nonce, idBytes}, nil)
        return ethcrypto.Keccak256(payload), nil
}
```

## Replay window

`Server` maintains an in-memory nonce guard that rejects any nonce observed in
the last ten minutes (`handshakeReplayWindow`). This guard applies to both
locally generated and remote nonces, providing a coarse replay window until the
full anti-replay design (NET-2G) lands. In addition to the moving window, the
guard now stores a cryptographic fingerprint of every `(nodeID, nonce)` pair it
has seen, so a captured handshake cannot be replayed even after the original
window has expired.

### Nonce guard internals

The guard is implemented by `nonceGuard` (`p2p/nonce.go`). Each nonce is stored
in an LRU list keyed by the raw hex string. When `Remember` is invoked:

1. Entries older than the configured window are pruned from the tail of the list
   (default: 10 minutes).
2. A fingerprint keyed by `nodeID||nonce` is checked against the persistent set.
   If it already exists the handshake is rejected and logged for operator
   visibility.
3. Otherwise a new record `{nonce, seenAt}` is inserted at the head of the list
   and the fingerprint is stored for future comparisons.

The structure is effectively bounded by the number of handshakes observed within
the ten minute window. Even at 1,000 handshakes per minute this amounts to ~10k
entries, easily handled in memory. Operators can reduce the window with
`P2P.HandshakeReplayWindow` once that configuration surface lands; until then the
default offers conservative protection without noticeable memory pressure.

In addition to nonce tracking, peers that fail handshake validation accrue
reputation penalties and may be temporarily banned depending on the configured
policy.

## RPC perimeter expectations

RPC services inherit the same perimeter assumptions as the P2P layer. Operators
must either terminate TLS directly on the node via `RPCTLSCertFile` /
`RPCTLSKeyFile` or place a mutually authenticated proxy in front of the HTTP
listener. When a proxy is used, declare its addresses in `RPCTrustedProxies`
before enabling `RPCTrustProxyHeaders`; all other callers will have their
`X-Forwarded-For` headers ignored, preventing spoofed client identities. The
server enforces a five-transaction-per-minute quota per resolved client source
and will return HTTP 429 with code `-32020` when exceeded, so tooling should
surface retry-after guidance alongside the existing P2P rate-limit telemetry.
Timeouts configured through `RPCReadHeaderTimeout`, `RPCReadTimeout`,
`RPCWriteTimeout`, and `RPCIdleTimeout` now gate the full request lifecycle, and
should be aligned with upstream load-balancer settings to avoid premature
disconnects.

## Ban reasons & operator guidance

| Event | Trigger | Default action | Operator notes |
| ----- | ------- | -------------- | -------------- |
| Handshake violation | Chain/genesis mismatch, signature failure, nonce replay | Immediate ban for `PeerBanDuration` (15m default) and peerstore entry marked via `RecordViolation`. | Verify the remote `nodeId` and published chain parameters before unbanning. Persistent mismatches usually indicate misconfiguration or an attempted Sybil. |
| Invalid message rate | >50% invalid messages within 5-frame window (`invalidRateThresholdPerc`) | Disconnect and ban if repeated; log `Protocol violation from <id>` with reason. | Inspect application logs for malformed payloads. If caused by a buggy release, roll back before whitelisting the peer. |
| Per-peer rate limit | Message throughput exceeds configured `RateMsgsPerSec` | Disconnect, optionally ban if reputation drops below `BanScore`. | Increase per-peer rate limits only if the remote is a trusted bulk publisher. Otherwise the throttle prevents spam amplification. |
| Global rate cap | Aggregate throughput exceeds `RateMsgsPerSec * MaxPeers` | Connection dropped (`global rate cap exceeded`) without banning. | Typically symptomatic of DDoS attempts. Raise global caps cautiously and monitor CPU load. |
| Manual ban (`peerstore.SetBan`) | Operator action via tooling | Persisted until expiry. | Use to quarantine peers for investigative or legal reasons. Document ban rationale for future audits. |

All bans honour the configured `PeerBanDuration` unless overridden by the
peerstore entry. Persistent peers are re-dialled automatically once the ban
expires.

## Security event log taxonomy

Operational logs form the primary audit trail for network policy decisions. The
table below summarises the high-signal log messages emitted by the P2P layer:

| Log snippet | Source | Meaning |
| ----------- | ------ | ------- |
| `Inbound connection from <addr> rejected: <err>` | `server.handleInbound` | Handshake failed. `err` contains specifics (chain mismatch, signature, timeout, nonce replay). |
| `Handshake nonce replay from <id> rejected` | `verifyHandshake` | A peer attempted to reuse a signed handshake and was banned. Monitor to detect replay probes. |
| `Protocol violation from <id>: <err> (score X)` | `handleProtocolViolation` | Peer sent malformed or unauthorized messages. Reputation adjusted accordingly. |
| `Peer <id> exceeded rate limit (score X)` | `handleRateLimit` | Per-peer message rate exceeded allowance; connection dropped and potentially banned. |
| `Dropping message from <id> due to global rate cap` | `handleRateLimit` (global) | The server hit its aggregate throughput budget and disconnected the peer without banning. |
| `Peer <id> disconnected and banned: <reason>` | `removePeer` | Final disposition when a peer crosses the ban threshold. Includes the rationale recorded in reputation/peerstore. |
| `Record handshake violation <id>: <err>` / `record handshake ban` | `markHandshakeViolation` | Persistence layer acknowledgement that the peer was banned for handshake faults. |

Collect these messages alongside RPC access logs to produce a full audit trail.
For production deployments forward them to a SIEM or long-term log store so that
investigations can correlate P2P events with application-level symptoms.
