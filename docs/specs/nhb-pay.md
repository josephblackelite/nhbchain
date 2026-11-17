# NHB Pay point-of-sale intents

A POS intent is a set of optional fields on an ordinary signed transaction that
give it a single-use reference and a deadline. The chain enforces the reference
and deadline; the deep-link/QR URI format described later is a client convention
implemented only in the SDK example, not something the node parses or verifies.

## On-chain fields

Fields of `types.Transaction` (`core/types/transaction.go`). They are covered by
the signed transaction hash ([signing](../transactions/signing.md)). The same
values travel in the gRPC `TxEnvelope` as `intent_ref`, `intent_expiry`,
`merchant_addr`, `device_id`, `refund_of`.

| JSON field | Type | Meaning |
| --- | --- | --- |
| `intentRef` | bytes (base64 in `nhb_sendTransaction`) | Single-use reference. Maximum 1024 bytes at the transaction level; the intent registry accepts 1 to 64 bytes (`intent: invalid reference` otherwise). |
| `intentExpiry` | uint64, unix seconds | Absolute deadline. |
| `merchantAddr` | string | Free-form string, trimmed, stored as supplied; also selects the fee domain (see [fee policy](../fees/policy.md)) and is echoed in events. |
| `deviceId` | string | Opaque terminal identifier, echoed in events. |
| `paymaster` | 20-byte address | Gas sponsor, not a POS label. Requires `paymasterR/S/V`, a signature by that address over the same hash; the node's paymaster module must be enabled. |
| `refundOf` | string | Origin transaction hash for a refund; see [refunds](./refunds.md). |

There is no `amount` or `currency` field. The amount of an NHB or ZNHB transfer is
the transaction's `value` (wei, an integer); no currency code exists on-chain.

## Rules enforced when a transaction has an `intentRef`

`executeTransaction` (`core/state_transition.go`, reached through `ApplyTransaction`)
calls `IntentRegistryValidate` (`core/state/intent_registry.go`) with the block
timestamp:

* `intentExpiry` must be non-zero and greater than the block time, else
  `intent: expired`.
* If the reference already exists in the registry: `intent: expired` when its
  stored expiry has passed (the record is then deleted), otherwise `intent:
  already consumed`.
* The stored expiry is `min(intentExpiry, block_time + 24h)` (`defaultIntentTTL`).
* After the transaction applies successfully the reference is stored as consumed
  under `pos/intent/<ref>` and a `payments.intent_consumed` event is emitted. A
  failed transaction does not consume the reference.

Mempool admission (`Node.addTransaction`) also rejects a transaction whose
`intentExpiry` is in the past by wall-clock time. For transactions without an
`intentRef`, a non-zero `intentExpiry` in the past by block time fails execution
with `ErrTransactionExpired`.

Every transaction type can carry these fields; NHB and ZNHB transfers with an
`intentRef` are additionally routed to the POS mempool lane
([QoS](./pos-qos.md)).

## Event `payments.intent_consumed`

Attributes (`core/events/payments.go`): `intentRef` (lowercase hex, no `0x`),
`txHash` (`0x`-prefixed), and `merchantAddr` and `deviceId` when set.

## URI convention used by the SDK example

`sdk/pos/examples/create_intent.go` and `submit_and_watch.ts` build the following.
Nothing in the node verifies this format or the signature.

```
nhbpay://intent/<intent_ref_hex>?amount=<decimal>&currency=<code>&expiry=<unix_seconds>&merchant=<address>[&paymaster=<value>][&device=<value>][&sig=<hex>]
```

* Query values are percent-escaped for the characters space, `"`, `#`, `%`, `&`,
  `+`, `/`, `=`, `?` (Go example) or with `encodeURIComponent` (TypeScript
  example).
* The signed string is `nhbpay://intent/<ref hex>?amount=..&currency=..&expiry=..&merchant=..`
  followed by `&device=..` and `&paymaster=..` when present, in that order
  (values unescaped). The example signs its UTF-8 bytes with Ed25519 and puts the
  hex signature in `sig`.
* In the example `paymaster` is an arbitrary string; it is unrelated to the
  on-chain `paymaster` address field.
