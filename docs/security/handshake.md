# Handshake Hardening

The peer-to-peer handshake signs a deterministic digest to authenticate peers and
protect replay windows. Version 1 of the protocol hashes the following
components:

```
keccak256(
    "nhb-handshake-v1" ||
    uint64(chain_id)   ||
    genesis_hash       ||
    nonce              ||
    canonical_node_id
)
```

* **Domain separation** – The constant `nhb-handshake-v1` ensures the signature
  cannot be replayed across other protocols or message formats that use the same
  primitives.
* **Canonical node identity** – The node ID must be provided in canonical
  lowercase hex form (`0x` prefix, 20-byte payload). This value is embedded as a
  string in the digest to bind the signature to the claimed peer identity.

## Nonce Canonicalisation

Handshake nonces are canonically encoded using the following normalisation:

1. Apply Unicode NFKC normalisation.
2. Strip zero-width format characters.
3. Trim Unicode whitespace.
4. Remove any leading `0x`/`0X` prefixes.
5. Lowercase all hexadecimal characters.
6. Left-pad to an even number of nibbles and decode as hex.
7. Re-encode the resulting bytes as lowercase hex.

Any deviation from this canonical form (extra prefixes, uppercase casing, odd
lengths, zero-width padding) is rejected during verification. The replay guard
stores the canonical nonce string, preventing attackers from bypassing replay
windows by mutating textual variants of the same nonce.
