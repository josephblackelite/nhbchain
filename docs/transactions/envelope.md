# Transaction Envelopes

The consensus service accepts module transactions through a single `TxEnvelope`
shape. Each envelope wraps the module-specific payload and attaches the metadata
required for replay protection and fee accounting. The protobuf definition lives
at [`proto/consensus/v1/tx.proto`](../../proto/consensus/v1/tx.proto).

```
message TxEnvelope {
  google.protobuf.Any payload = 1;
  uint64 nonce = 2;
  string chain_id = 3;
  Fee fee = 4;
  string memo = 5;
}
```

* **payload** – an [`Any`](https://protobuf.dev/programming-guides/any/) pointing
  at the module message (for example `lending.v1.MsgBorrow`). The SDK helpers
  normalise the `type_url` to `type.googleapis.com/<fully-qualified-name>`.
* **nonce** – monotonically increasing per-sender sequence number used to guard
  against replay attacks.
* **chain_id** – canonical identifier for the target network (e.g. `localnet`).
* **fee** – optional fee declaration. When omitted the transaction is treated as
  fee-free.
* **memo** – human readable note stored alongside the transaction.

## Constructing envelopes

The Go SDK exposes a [`consensus.NewTx`](../../sdk/consensus/tx.go) helper that
accepts the module payload, nonce and chain identifier. Optional parameters for
fees and memo strings can be provided; blank values result in the field being
omitted entirely.

Each module ships with dedicated builders that validate domain specific rules
before returning the protobuf payload. For example, the lending SDK exposes
[`lending.NewMsgBorrow`](../../sdk/lending/tx.go) which verifies the amount is a
positive integer and trims all string inputs before constructing the
`MsgBorrow` message.

TypeScript clients follow the same shape by encoding the module payload with the
generated helpers and wrapping the bytes in an `Any` record. The example at
[`examples/txs/ts/supply.ts`](../../examples/txs/ts/supply.ts) demonstrates the
full flow.
