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
Dialing  -->  Handshaking  -->  Connected
   ^            |               |
   |            v               v
Reconnect   Reject/Ban      Keepalive
```

* **Dialing** – initiated either manually (`Connect`) or by the connection
  manager.
* **Handshaking** – performs the authenticated handshake exchange and enforces
  policy (chain, genesis, signature, nonce replay).
* **Connected** – schedules read/write loops, activates PING/PONG keepalive, and
  feeds traffic into the application handler.

Peers that violate protocol expectations during any phase are disconnected and
optionally banned according to the configured reputation policy.
