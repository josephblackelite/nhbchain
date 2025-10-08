# Signing transaction envelopes

Once a `TxEnvelope` has been prepared it must be signed before the consensus
service will accept it. The Go SDK provides a [`consensus.Sign`](../../sdk/consensus/tx.go)
helper that performs the canonical encoding and secp256k1 signing routine used
by the validators.

1. The envelope is marshalled with Protocol Buffers.
2. A SHA-256 digest of the bytes is produced.
3. The digest is signed with the caller's secp256k1 private key.
4. The raw 65-byte signature and compressed public key are embedded into a
   `TxSignature` record.

The helper returns a `SignedTxEnvelope` ready for broadcast via the
[`Client.SubmitEnvelope`](../../sdk/consensus/client.go) method or through the
higher level [`consensus.Submit`](../../sdk/consensus/tx.go) convenience
function.

TypeScript clients should mirror this process, ensuring they serialise the
`TxEnvelope` using the generated helpers before hashing and signing. The example
in [`examples/txs/ts/supply.ts`](../../examples/txs/ts/supply.ts) computes the
transaction digest and highlights the placeholder where wallet integrations
should inject the signature.

For concrete JSON-RPC payloads that cover both NHB (`TxTypeTransfer`) and the
new ZNHB (`TxTypeTransferZNHB`) transfers, see
[`znhb-transfer.md`](./znhb-transfer.md). It includes copy-paste ready requests
with populated `r`/`s`/`v` signature components, nonce discovery, and the
expected receipt payload so wallet developers can mirror the node's behaviour.
