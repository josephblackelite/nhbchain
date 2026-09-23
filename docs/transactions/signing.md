# Signing transactions

## Native transactions (`nhb_sendTransaction`)

Native transactions (`types.Transaction`, `core/types/transaction.go`) are signed
with secp256k1. For any `type` greater than zero the signed hash
(`Transaction.Hash`) is SHA-256 over this canonical byte string, in order:

| Bytes | Content |
| --- | --- |
| ASCII | `NHB_TX_V3_MAINNET` |
| 8 | chain id, big-endian uint64 (`0x4e4842`) |
| 1 | transaction type |
| 8 | `nonce` |
| 8 | `maxBlockHeight` |
| 8 | `intentExpiry` |
| 2 + n | `intentRef` (uint16 length, then bytes) |
| 20 | `to`, left-padded with zeros |
| 2 + n | `value` (uint16 length of the big-endian bytes, then bytes; length 0 for nil/zero) |
| 4 + n | `data` (uint32 length, then bytes) |
| 8 | `gasLimit` |
| 2 + n | `gasPrice`, encoded like `value` |
| 1 (+20) | `0` if no paymaster, otherwise `1` followed by the 20-byte paymaster address |
| 4 + n | `merchantAddr` (trimmed; uint32 length, then bytes) |
| 4 + n | `deviceId` (same encoding) |
| 4 + n | `refundOf` (same encoding) |

The signature is `secp256k1.Sign(hash, privateKey)` (go-ethereum `crypto.Sign`)
split into `r` and `s` (32 bytes each) and `v = recoveryId + 27`. The sender is
recovered from `(hash, r, s, v)`; `s` must be at most half the curve order
(otherwise `invalid signature: S > secp256k1n/2`), and `r`/`s` must not exceed 32
bytes. The transaction hash returned by `nhb_sendTransaction` is this same
SHA-256 value; it does not cover `r`, `s` or `v`.

A paymaster (gas sponsor), when present, signs the same hash and supplies
`paymasterR`, `paymasterS`, `paymasterV`; the recovered address must equal the
`paymaster` field.

Go code that signs: `Transaction.Sign(privateKey)`; `nhb-cli send-nhb`,
`send-znhb`, `gov ...`, and the other CLI commands build and sign transactions
this way (`cmd/nhb-cli`). The full JSON encoding accepted by the RPC is in
[rpc.md](../api/rpc.md#transaction-encoding).

## Consensus envelopes (`SubmitTxEnvelope`)

`consensus.Sign` (`sdk/consensus/tx.go`) signs a `TxEnvelope`
([envelope](./envelope.md)):

1. The envelope is marshaled with `proto.Marshal`.
2. `digest = sha256(bytes)`.
3. `signature = secp256k1.Sign(digest, key)` (go-ethereum `crypto.Sign`, 65 bytes,
   recovery id 0 or 1 in the last byte).
4. `TxSignature.public_key` is the **uncompressed** public key
   (`crypto.FromECDSAPub`, 65 bytes); `TxSignature.signature` is the 65-byte
   signature.

The server (`consensus/codec.TransactionFromEnvelope`) recomputes the digest and
verifies the first 64 bytes of `signature` against `public_key`.
`consensus.Submit` signs (when given an unsigned envelope and a key) and sends
through `Client.SubmitEnvelope`.

`examples/txs/ts/supply.ts` computes the SHA-256 digest of the encoded envelope and
uses a placeholder random signature; it does not sign.

For NHB and ZNHB transfer payloads see
[znhb-transfer.md](./znhb-transfer.md).
