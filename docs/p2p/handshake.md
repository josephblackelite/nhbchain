# NHB P2P Handshake

The NHB peer handshake provides mutual authentication, network compatibility
checks, and Sybil resistance by binding node identities to funded wallets. All
connections MUST complete the handshake before any protocol traffic is
exchanged.

## Message layout

Handshake frames are newline-delimited JSON objects exchanged immediately after
the TCP connection is established. Each side sends exactly one message:

```json
{
  "protocolVersion": 1,
  "chainId": 187001,
  "genesisHash": "0x5e2c…7fa1",
  "nodeId": "nhb1u8…r9fz",
  "clientVersion": "nhbchain/node",
  "pubKey": "0x04…",
  "nonce": "0x6d94…",
  "signature": "0x68a4…",
  "walletAddress": "nhb1u8…r9fz",
  "walletSignature": "0xacd1…"
}
```

Field meanings:

| Field | Description |
| --- | --- |
| `protocolVersion` | Static protocol identifier (currently `1`). Peers that send an unknown version are rejected. |
| `chainId` | Chain identifier. Must match the local node's chain ID. |
| `genesisHash` | Canonical genesis block hash. Prevents forks joining the wrong network. |
| `nodeId` | Optional claimed node ID. If present it must match the ID derived from `pubKey`. |
| `clientVersion` | Free-form client software/version string. Exposed via RPC. |
| `pubKey` | Uncompressed secp256k1 public key (hex). Used to derive the node ID and verify `signature`. |
| `nonce` | 32-byte random challenge unique to this handshake. |
| `signature` | ECDSA signature produced by the node key over the handshake digest (see below). |
| `walletAddress` | Bech32 NHB address the node is bound to. Must equal the address derived from `pubKey`. |
| `walletSignature` | ECDSA signature produced by the wallet key over the wallet-binding digest. |

### Digests and signatures

* **Handshake digest:** `SHA256(protocolVersion || chainId || genesisHash || clientVersion || nonce)`.
  The sender signs this digest with their node private key and includes the
  signature in `signature`.
* **Wallet binding digest:** `SHA256("nhb-handshake" || protocolVersion || chainId || genesisHash || lower(nodeId) || nonce)`.
  The sender signs this digest with the wallet key and includes the signature in
  `walletSignature`.

Both signatures must be 65 bytes (Ethereum-style `R || S || V`). The wallet
signature is recovered to an Ethereum address and compared byte-for-byte with
the decoded Bech32 address in `walletAddress`.

## Handshake flow

1. **Connection accepted/dialed.** Both sides open a TCP connection and wrap it
   with buffered IO.
2. **Handshake send.** Each side generates a new random nonce, builds the JSON
   payload, and transmits it followed by a newline.
3. **Handshake receive.** The peer reads the incoming frame and performs the
   following checks in order:
   * Correct nonce length.
   * Protocol version equals the local version.
   * Signature validates against the provided public key.
   * Derived node ID matches the claimed `nodeId` (if present).
   * Wallet signature validates and the address matches the derived node ID.
   * `chainId` matches the local chain.
   * `genesisHash` matches the local genesis hash.
4. **Duplicate / self check.** Connections claiming the local node ID or
   already-connected wallet address are rejected.
5. **Registration.** Upon successful verification, the peer is registered and
   message streams begin.

A 5 second handshake timeout (`HandshakeTimeoutMs` in `config.toml`) protects
against slow or unresponsive peers.

## Failure codes

Peers are immediately disconnected with the following categories of errors:

* **Version/compatibility failures:** unsupported protocol version, mismatched
  chain ID, or mismatched genesis hash.
* **Authentication failures:** malformed signatures, wallet signature mismatch,
  or node ID collision.
* **Policy failures:** attempts to connect while banned, or claiming a wallet
  address already bound to another connection.

These failures are logged and counted toward the peer's reputation score.
