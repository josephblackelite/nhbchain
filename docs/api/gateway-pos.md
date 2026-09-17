# POS gateway HTTP API

The POS gateway (`gateway/routes/*.go`) does not expose an `/api/pos/*` HTTP
REST surface. POS payment submission and status lookup are handled by two
separate, real interfaces instead:

For MDR, free-tier, and routing expectations that apply to POS payments, see
[fee policy](../fees/policy.md) and [fee routing](../fees/routing.md).

## Submitting a payment (gRPC)

POS authorize/capture/void requests are submitted through the `pos.v1.Tx`
gRPC service defined in [`proto/pos/tx.proto`](../../proto/pos/tx.proto) and
implemented in [`rpc/pos_grpc.go`](../../rpc/pos_grpc.go):

| RPC | Request | Response |
| --- | --- | --- |
| `AuthorizePayment` | `MsgAuthorizePayment` (`payer`, `merchant`, `amount`, `expiry`, `intent_ref`, `nonce`, `expires_at`, `chain_id`) | `MsgAuthorizePaymentResponse` (`authorization_id`) |
| `CapturePayment` | `MsgCapturePayment` (`merchant`, `authorization_id`, `amount`, `nonce`, `expires_at`, `chain_id`) | `MsgCapturePaymentResponse` (`authorization_id`, `captured_amount`, `refunded_amount`) |
| `VoidPayment` | `MsgVoidPayment` (`merchant`, `authorization_id`, `reason`, `nonce`, `expires_at`, `chain_id`) | `MsgVoidPaymentResponse` (`authorization_id`, `refunded_amount`, `expired`) |

See [the POS intent spec](../specs/nhb-pay.md) and
[`docs/specs/pos-lifecycle.md`](../specs/pos-lifecycle.md) for the payload
format and the authorize/capture/void lifecycle these calls drive.

## Looking up authorization status (JSON-RPC)

To resolve an authorization from a client-supplied `intentRef`, call
`pos_getAuthorizationByIntentRef` on the node's JSON-RPC server (`cmd/nhb`,
not the gateway). It takes a single hex-encoded `intentRef` parameter, does a
direct lookup against node state, and returns a `POSAuthorizationResult`
(`id`, `payer`, `merchant`, `amount`, `capturedAmount`, `refundedAmount`,
`expiry`, `intentRef`, `status`, `createdAt`) or `null` if unknown. It does
not combine a gateway submission log with realtime finality updates.

```jsonc
{
  "id": 1,
  "jsonrpc": "2.0",
  "method": "pos_getAuthorizationByIntentRef",
  "params": ["0x2d8c7fd3e1a94f4c998e4cfedc3a4567bb12aa09887766554433221100ff9a01"]
}
```

## Polling strategy

Prefer the realtime gRPC/WebSocket stream ([`docs/api/pos-realtime.md`](pos-realtime.md))
for live updates, and only fall back to polling `pos_getAuthorizationByIntentRef`
during recovery or when a terminal cannot maintain a streaming connection.
