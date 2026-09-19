# Signing transactions

> This page covers signing a consensus `TxEnvelope` for the consensus gRPC
> service (`sdk/consensus`). It does not describe the transaction hash used by the
> `nhb_sendTransaction` JSON-RPC method, which is the SHA-256 of the
> `NHB_TX_V3_MAINNET` binary encoding (`Transaction.Hash`,
> `core/types/transaction.go`). For that, see the [wallet builder
> guide](../sdk/wallets.md).

Once a `TxEnvelope` has been prepared it must be signed before the consensus
service will accept it. The Go SDK provides a [`consensus.Sign`](../../sdk/consensus/tx.go)
helper that performs the canonical encoding and secp256k1 signing routine used
by the validators.

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

For concrete `nhb_sendTransaction` JSON-RPC payloads that cover both NHB
(`TxTypeTransfer`) and ZNHB (`TxTypeTransferZNHB`) transfers, see
[`znhb-transfer.md`](./znhb-transfer.md) and the
[Sending ZNHB via `nhb_sendTransaction`](../api/rpc.md#sending-znhb-via-nhb_sendtransaction)
example. Those transactions are signed with the `nhb_sendTransaction` hash
described in the [wallet builder guide](../sdk/wallets.md), not with the envelope
digest on this page.
