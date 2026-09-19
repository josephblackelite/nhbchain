# POS payment submission and lookup

There is no `/api/pos/*` HTTP surface: `gateway/routes/` contains lending,
proxy, transactions and wallet routes only. POS authorize, capture and void are
native transactions sent to the node, and status is read with JSON-RPC.

For fee behaviour on POS payments see [fee policy](../fees/policy.md) and
[fee routing](../fees/routing.md).

## Submitting authorize, capture and void

The `pos.v1.Tx` and `pos.v1.Registry` gRPC services are defined in
[`proto/pos/tx.proto`](../../proto/pos/tx.proto) and
[`proto/pos/registry.proto`](../../proto/pos/registry.proto), but they are **not
registered** on the node: `Serve` in `rpc/http.go` registers only
`pos.v1.Realtime`. A call to the Tx service fails with `Unimplemented`. The
implementation in `rpc/pos_grpc.go` is unused because its messages carry no
signature, and the state machine requires the transaction signer to be the party
named in the message.

The working path is `nhb_sendTransaction` with these transaction types
(`core/types/transaction.go`, dispatch in `core/state_transition.go`,
handlers in `core/state_pos.go`):

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

## Live updates

Use the finality stream ([pos-realtime.md](pos-realtime.md)) for live status and
fall back to `pos_getAuthorizationByIntentRef` for recovery.
