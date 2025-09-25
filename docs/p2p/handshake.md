# NHB P2P Handshake

> **Note**
>
> The NET-2A handshake is documented in detail under
> [`docs/networking/overview.md`](../networking/overview.md). The material below
> captures the earlier NET-1 design and is retained for historical reference.

The NHB peer handshake provides mutual authentication, network compatibility
checks, and Sybil resistance by binding node identities to funded wallets. All
connections MUST complete the handshake before any protocol traffic is
exchanged.

## Message layout

Handshake frames are newline-delimited JSON objects exchanged immediately after
the TCP connection is established. Each side sends exactly one message:

```json
{
  "protoVersion": 1,
  "chainId": 187001,
  "genesisHash": "0x5e2c…7fa1",
  "nodeIdPub": "0x04…",
  "nodeAddrBech32": "nhb1u8…r9fz",
  "nonce": "0x6d94…",
  "ts": 1710950400,
  "clientVersion": "nhbchain/node",
  "sigAddr": "nhb1u8…r9fz",
  "sig": "0x68a4…"
}
```

Field meanings:

| Field | Description |
| --- | --- |
| `protoVersion` | Static protocol identifier (currently `1`). Peers that send an unknown version are rejected. |
| `chainId` | Chain identifier. Must match the local node's chain ID. |
| `genesisHash` | Canonical genesis block hash. Prevents forks joining the wrong network. |
| `nodeIdPub` | Uncompressed secp256k1 public key (hex). Used to derive the node ID and verify `sig`. |
| `nodeAddrBech32` | Bech32 NHB/ZNHB address bound to this node. The derived address from `nodeIdPub` must match. |
| `nonce` | 32-byte random challenge unique to this handshake. Nonces are cached for 10 minutes to prevent replay. |
| `ts` | Unix timestamp (seconds). Must be within ±300 seconds of the local clock. |
| `clientVersion` | Free-form client software/version string exposed via RPC. |
| `sigAddr` | Wallet address recovered from the signature. It MUST equal `nodeAddrBech32`. |
| `sig` | EIP-191 signature produced by the wallet key over the handshake digest (see below). |

### Digests and signatures

* **Payload JSON:** serialize the message without the signature fields. This
  payload is transported verbatim and referenced when signing.
* **Handshake digest:** `keccak256("nhb-p2p|hello|" + payload + "|" + ts)`. The
  digest is signed with the wallet (node) key using the standard
  `eip191_sign`/`personal_sign` scheme, yielding a 65-byte signature stored in
  `sig`.
* **Header mapping:** `sigAddr` corresponds to the HTTP-style header
  `X-Sig-Addr`, while `sig` corresponds to `X-Sig` for systems that break the
  handshake apart into metadata and payload.

The recovered Ethereum address from `sig` is compared byte-for-byte with the
decoded Bech32 address in `nodeAddrBech32`. Any mismatch terminates the
handshake.

## Handshake flow

1. **Connection accepted/dialed.** Both sides open a TCP connection and wrap it
   with buffered IO.
2. **Handshake send.** Each side generates a new random nonce, stamps the
   current Unix time, builds the JSON payload, signs it, and transmits it
   followed by a newline.
3. **Handshake receive.** The peer reads the incoming frame and performs the
   following checks in order:
   * Correct nonce length and previously unseen nonce.
   * Protocol version equals the local version.
   * Timestamp within ±300 seconds of the local clock.
   * Public key decodes to an NHB/ZNHB address that matches `nodeAddrBech32`.
   * Signature validates over the payload digest and recovers the claimed
     address.
   * `chainId` matches the local chain.
   * `genesisHash` matches the local genesis hash.
4. **Duplicate / self check.** Connections claiming the local node ID or wallet
   address are rejected. Persistent metadata (`firstSeen`, `lastSeen`,
   `clientVersion`) is stored for RPC reporting.
5. **Registration.** Upon successful verification, the peer is registered and
   message streams begin.

A 5 second handshake timeout (`HandshakeTimeoutMs` in `config.toml`) protects
against slow or unresponsive peers.

## Failure codes

Peers are immediately disconnected with the following categories of errors:

* **Version/compatibility failures:** unsupported protocol version, mismatched
  chain ID, mismatched genesis hash, or timestamps outside the allowed skew.
* **Authentication failures:** malformed signatures, signature/address mismatch,
  or node ID collision.
* **Policy failures:** attempts to connect while banned, nonce replays, or
  claiming a wallet address already bound to another connection.

These failures are logged and counted toward the peer's reputation score.
