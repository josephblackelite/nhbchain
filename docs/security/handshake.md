# Handshake Hardening

The peer-to-peer handshake (`p2p/handshake.go`) authenticates peers with a signed digest and rejects replayed handshakes. This page describes protocol version 1.

## Message

Each side writes one newline-terminated JSON object, then reads the peer's. The object carries:

| Field | Meaning |
| --- | --- |
| `protoVersion` | Must equal `1`. |
| `chainId` | Must equal the local chain ID. |
| `genesisHash` | `0x` hex; must equal the local genesis hash. |
| `nodeId` | Canonical node ID (below). |
| `nonce` | 12 random bytes, `0x`-prefixed lowercase hex. |
| `clientVersion` | Required, non-empty. |
| `listenAddrs` | Optional list of `host:port` strings; entries with an empty host, port `0` or an unspecified IP are dropped. Only the first 64 entries are examined and at most 8 are kept (`maxHandshakeListenAddrScan`, `maxHandshakeListenAddrs` in `p2p/handshake.go`). |
| `sig` | 65-byte recoverable secp256k1 signature, hex. |

A frame larger than the configured maximum message size (default 1 MiB) is rejected with `handshake frame exceeds maximum size`.

## Signed digest

```
keccak256(
    "nhb-handshake-v1"   ||   // ASCII bytes
    uint64_be(chain_id)  ||   // 8 bytes
    genesis_hash         ||   // raw bytes
    nonce                ||   // the 12 raw bytes
    canonical_node_id         // the ASCII string, e.g. "0x1a2b..."
)
```

Source: `handshakeDigest` in `p2p/handshake.go`.

* **Domain separation.** The constant `nhb-handshake-v1` ties the signature to this protocol.
* **Node identity.** A node ID is `0x` followed by the lowercase hex of `keccak256` over the 64-byte uncompressed public key (without the `0x04` prefix), that is 32 bytes (`deriveNodeIDFromPub` in `p2p/identity.go`). The receiver recovers the public key from `sig`, derives the node ID, and requires it to equal `nodeId`. The node ID is hashed as a string, so it must already be canonical (`0x` prefix, lowercase); any other spelling is rejected with `handshake node ID must be canonical hex`.

## Nonce canonicalisation

`canonicalizeNonce` in `p2p/nonce.go` normalises a nonce as follows:

1. Apply Unicode NFKC normalisation.
2. Drop every rune in Unicode category Cf (format characters, which includes zero-width characters).
3. Trim surrounding whitespace (`strings.TrimSpace`).
4. Lowercase.
5. Strip any number of leading `0x` prefixes (trimming whitespace after each).
6. If the length is odd, left-pad with one `0`.
7. Decode as hex and re-encode as lowercase hex.

Empty results and non-hex input are rejected. The handshake is stricter than the canonicaliser: after canonicalising, the decoded nonce must be exactly 12 bytes and the `nonce` field must equal `0x` plus its lowercase hex, otherwise verification fails (`invalid nonce encoding`, `invalid handshake nonce length`, `handshake nonce must use canonical encoding`).

## Replay guard

After the signature checks pass, the receiver records `(node ID, canonical nonce)` in a nonce guard (`p2p/nonce_guard.go`). The guard key is the SHA-256 of `<node id>:<canonical nonce>`, so textual variants of one nonce map to the same entry. A nonce seen again inside the window is rejected with `handshake nonce replay detected`.

* The server creates its guard with a 10 minute window (`handshakeReplayWindow` in `p2p/handshake.go`).
* The guard holds at most 100,000 entries; the oldest are evicted first (`defaultNonceGuardMaxEntries` in `p2p/nonce_guard.go`). A background janitor removes expired entries.
* The nonce for a locally built handshake is recorded too, and a collision fails the build (`nonce collision detected`).

## Violations and bans

`markHandshakeViolation(nodeID, authenticated)` bans a node ID only when `authenticated` is true, that is, when the packet's signature really recovers to the node ID the packet names (`handshakeSignedByClaimedNode`). The node ID in a packet is a string the sender chose, so a packet that is not signed by that node is never held against it.

* **Banned (when the packet is signed by the claimed node):** chain ID mismatch and genesis hash mismatch. The ban is applied in the reputation tracker and the peer store for `[p2p].BanDurationSeconds` (when unset it takes the top-level `PeerBanSeconds`, which defaults to 3600 seconds in `config/config.go`; `p2p/handshake.go` falls back to 15 minutes, `defaultPeerBan`, only if it is handed a non-positive duration), and a violation is recorded in the peer store.
* **Refused without a ban:** any signature failure (bad encoding, wrong length, recovery failure, node ID mismatch), and nonce replay. A replayed handshake is a copy that anyone who saw the original could send, so nothing is held against the node that signed it.
* **Also refused without a ban:** unsupported protocol version, missing fields, non-canonical node ID or nonce.
* **Never banned on handshake evidence:** a node ID that is a configured persistent peer (`isConfiguredPersistentPeer`), so that it can reconnect as soon as its fault is fixed.
