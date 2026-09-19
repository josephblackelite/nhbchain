# POS payment submission and lookup

There is no `/api/pos/*` HTTP surface: `gateway/routes/` contains lending,
proxy, transactions and wallet routes only. POS authorize, capture and void are
native transactions sent to the node, and status is read with JSON-RPC.

For fee behaviour on POS payments see [fee policy](../fees/policy.md) and
[fee routing](../fees/routing.md).

## Submitting a payment

The `pos.v1.Tx` gRPC service (`AuthorizePayment`, `CapturePayment`,
`VoidPayment`, defined in [`proto/pos/tx.proto`](../../proto/pos/tx.proto)) is
**retired** and is not registered on the node's RPC server (`rpc/http.go`
`Serve`, NHB-AUDIT-S3); calls return `Unimplemented`. Its messages carry no
signature field, so the service could never produce an authorized payment.

Payments are submitted as native transactions signed client-side with the
payer's or merchant's own wallet key and sent through the standard
`nhb_sendTransaction` JSON-RPC method:

| Operation | Transaction type |
| --- | --- |
| Authorize | `TxTypePOSAuthorize` (`0x20`) |
| Capture | `TxTypePOSCapture` (`0x21`) |
| Void | `TxTypePOSVoid` (`0x22`) |

For an authorize transaction the payer must be the transaction signer
(`core/state_pos.go` `applyPOSAuthorize`).

| Type | Value | `data` | Who must sign |
| --- | --- | --- | --- |
| `TxTypePOSAuthorize` | `0x20` | protobuf-encoded `MsgAuthorizePayment` | The `payer` named in the message (`pos: payer must match the transaction signer`). |
| `TxTypePOSCapture` | `0x21` | protobuf-encoded `MsgCapturePayment` | The merchant of the authorization (`pos: caller is not authorized for this authorization` otherwise). |
| `TxTypePOSVoid` | `0x22` | protobuf-encoded `MsgVoidPayment` | The payer or the merchant of the authorization. |

`data` is the raw protobuf bytes of the message (not wrapped in an `Any`). The
fields the state machine reads are:

* `MsgAuthorizePayment`: `payer`, `merchant` (bech32), `amount` (decimal string
  of ZNHB wei), `expiry` (unix seconds), `intent_ref` (bytes).
* `MsgCapturePayment`: `authorization_id`, `amount`.
* `MsgVoidPayment`: `authorization_id`, `reason`.

`authorization_id` must be the 64-character hex string of the 32-byte id, without
a `0x` prefix. The `nonce`, `expires_at`, `chain_id` and `merchant` fields in
these messages are not read by the transaction handlers. The lifecycle rules are
in [POS lifecycle](../specs/pos-lifecycle.md).

## Looking up authorization status (JSON-RPC)

Both methods are read-only, need no auth, take a single hex string parameter and
return a `POSAuthorizationResult` (`rpc/http.go`) or `null` when nothing is
found.

* `pos_getAuthorization` — param: the 32-byte authorization id as hex (`0x`
  prefix optional). Any other length returns `authorization id must be a 32-byte
  hex string`.
* `pos_getAuthorizationByIntentRef` — param: the non-empty hex `intentRef`
  embedded in the Authorize transaction. This is how a merchant discovers the
  authorization id after submitting an Authorize transaction (the id is derived
  by the chain, not chosen by the client).

```jsonc
{
  "id": 1,
  "jsonrpc": "2.0",
  "method": "pos_getAuthorizationByIntentRef",
  "params": ["0x2d8c7fd3e1a94f4c998e4cfedc3a4567bb12aa09887766554433221100ff9a01"]
}
```

Result fields: `id`, `payer`, `merchant` (bech32), `amount`, `capturedAmount`,
`refundedAmount` (decimal strings), `expiry`, `intentRef` (when set), `status`
(`pending`, `captured`, `voided` or `expired`), `createdAt`, `updatedAt`,
`voidReason` (when set), and `txHash`. `txHash` comes from the validator's
in-memory finality cache and is omitted after a restart or when this validator
never saw the intent; do not rely on it for reconciliation.

Prefer the realtime gRPC (`pos.v1.Realtime/SubscribeFinality`) or WebSocket stream ([`docs/api/pos-realtime.md`](pos-realtime.md))
for live updates, and only fall back to polling `pos_getAuthorizationByIntentRef`
during recovery or when a terminal cannot maintain a streaming connection.
