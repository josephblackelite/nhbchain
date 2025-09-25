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
full anti-replay design (NET-2G) lands.

In addition to nonce tracking, peers that fail handshake validation accrue
reputation penalties and may be temporarily banned depending on the configured
policy.
