# Transaction envelopes

There are two ways to submit a transaction to a node:

1. **JSON-RPC** `nhb_sendTransaction` with a native transaction object. This is
   the path used by the CLI and wallets; see [signing](./signing.md) and
   [rpc.md](../api/rpc.md#transaction-encoding).
2. **gRPC** `consensus.v1.ConsensusService/SubmitTxEnvelope` with a signed
   `TxEnvelope`, described here. It is served by `consensusd`.

The protobuf definitions are in
[`proto/consensus/v1/tx.proto`](../../proto/consensus/v1/tx.proto).

```
message TxEnvelope {
  google.protobuf.Any payload = 1;
  uint64 nonce = 2;
  string chain_id = 3;
  Fee fee = 4;
  string memo = 5;
  bytes intent_ref = 6;
  uint64 intent_expiry = 7;
  string merchant_addr = 8;
  string device_id = 9;
  string refund_of = 10;
}

message SignedTxEnvelope { TxEnvelope envelope = 1; TxSignature signature = 2; }
message TxSignature { bytes public_key = 1; bytes signature = 2; }
```

## How the node reads an envelope

`consensus/codec.TransactionFromEnvelope` (`consensus/codec/codec.go`):

1. Requires a body and a signature of at least 64 bytes and a non-empty
   `public_key`.
2. Computes `sha256(proto.Marshal(envelope))` and verifies the first 64 bytes of
   the signature against `public_key`. See [signing](./signing.md).
3. Decodes `payload` by its `type_url`. Only these are accepted:
   * `type.googleapis.com/consensus.v1.Transaction`: the payload carries a full
     native transaction (with its own `r`, `s`, `v`). If the envelope `nonce` is
     non-zero and the inner transaction's nonce is also non-zero, they must be
     equal (`envelope: nonce mismatch` otherwise); if the inner nonce is 0, the
     envelope nonce is copied into the transaction. A zero envelope nonce leaves
     the inner nonce unchanged.
   * `swap.v1.MsgPayoutReceipt`.
   * `pos.v1` messages (`MsgAuthorizePayment`, `MsgCapturePayment`,
     `MsgVoidPayment` and the registry messages).

   For the `swap.v1` and `pos.v1` payloads the codec builds a transaction with
   zero gas and no `r`/`s`/`v`, with the `Any` (marshaled) as `data`. Their
   transaction types require a recoverable sender (`types.RequiresSignature`),
   and mempool admission recovers it with `tx.From()`, so such a transaction has
   no sender to recover. The working POS path is `nhb_sendTransaction`
   ([POS gateway API](../api/gateway-pos.md)).
   * Any other `type_url` is rejected. The payload is first decoded with
     `payload.UnmarshalNew()`, so a type the binary's proto registry does not know
     fails with `envelope: decode module payload: ...`. A registered type that has
     no case in the codec fails with `envelope: unsupported module payload type`.
4. Copies `intent_ref`, `intent_expiry`, `merchant_addr`, `device_id` and
   `refund_of` from the envelope onto the resulting transaction, then submits it
   through the normal mempool admission path (`Node.SubmitTxEnvelope`).

The `fee` and `memo` fields are defined in the schema but are not read by any node
code path.

For the module payload types above, `nonce` becomes the transaction nonce
(including 0). For `consensus.v1.Transaction` it is checked against, or filled
into, the inner nonce as described in step 3. `chain_id` is parsed as a base-10 integer
for module payloads (`envelope: invalid chain id` otherwise); the network's chain
id is `0x4e4842` (`types.NHBChainID`).

## Constructing envelopes in Go

`sdk/consensus` provides:

* `consensus.NewTx(payload, nonce, chainID, feeAmount, feeDenom, feePayer, memo)`
  packs `payload` into an `Any`, trims strings, and sets `fee` only when at least
  one fee argument is non-empty. It does not set the intent fields (6 to 10).
* `consensus.Sign(envelope, key)` and `consensus.Submit(...)`
  ([signing](./signing.md)).

`sdk/lending` builds `lending.v1` messages (for example `lending.NewMsgBorrow`,
which requires a positive integer amount and trims its string inputs), and
`examples/txs/go/borrow.go` and `examples/txs/ts/supply.ts` wrap them in
envelopes. The node does not accept `lending.v1` payloads (see above), so those
examples build a valid envelope that `SubmitTxEnvelope` rejects. For lending
transactions use the native transaction types through `nhb_sendTransaction`, or
the lending daemon ([lending gRPC](../api/lending-grpc.md)).
