# NHB Pay Point-of-Sale Intents

This document captures the wire-level metadata that wallets and terminals use to
anchor point-of-sale (POS) payment intents on NHBchain. The consensus service
persists the same fields inside every `TxEnvelope`, enabling validators to
reject replayed payloads and to enforce a bounded settlement window.

## Envelope metadata

The following fields are attached to every POS envelope. They are signed along
with the underlying transaction to prevent tampering.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `intent_ref` | 32&nbsp;bytes | Merchant-generated nonce that uniquely identifies the payment intent. Terminals SHOULD generate a cryptographically random 32-byte value. Wallets and services MUST treat the field as opaque bytes on-chain. |
| `intent_expiry` | `uint64` (seconds) | Unix timestamp indicating the absolute expiry of the intent. Validators clamp the stored expiry to `min(intent_expiry, block_time + 24h)` and reject any payload where `now >= intent_expiry`. |
| `merchant_addr` | string | Canonical merchant address presented to the customer. The canonical representation is the NHB bech32 string (e.g. `nhb1...`). The consensus layer stores the trimmed string exactly as supplied. |
| `device_id` | string (optional) | Identifier emitted by the POS terminal. The value is opaque to consensus and is only surfaced in events for downstream telemetry. |

### Replay protection

* Every block stores the `intent_ref` with a consumed flag. Subsequent
  transactions referencing the same value fail with `ErrIntentConsumed`.
* Expired intents are rejected during mempool simulation and again during
  block execution with `ErrIntentExpired`.
* The registry clamps the persisted expiry to a 24-hour time-to-live (TTL)
  window to avoid unbounded growth.

## Canonical encodings

### QR / deep-link URI

Wallets SHOULD encode the metadata inside a URI with the following shape:

```
nhbpay://intent/<intent_ref_hex>?expiry=<unix_seconds>&merchant=<bech32>[&device=<url-encoded>]
```

* `<intent_ref_hex>` is the lowercase hex encoding of the 32-byte reference.
* `<unix_seconds>` is the decimal representation of `intent_expiry`.
* `<bech32>` is the canonical merchant address.
* The optional `device` parameter carries the terminal identifier. Values must
  be URL encoded. Unknown query parameters MUST be ignored by wallets.

### NFC payload

When transporting an intent over NFC the byte payload MUST follow the sequence
below:

```
intent_ref (32 bytes)
intent_expiry (uint64, big-endian)
merchant_addr (20 bytes)
[device_id_len (uint8) || device_id bytes]
```

* `merchant_addr` is the 20-byte account identifier obtained after decoding the
  bech32 representation supplied in the URI.
* `device_id` is optional; when present the leading byte encodes the length of
  the subsequent UTF-8 sequence.

Terminals and wallets MAY include additional vendor-specific data after the
canonical segment provided they agree on the extension. Consensus logic only
cares about the canonical fields listed in the table above.

## Events

The chain emits `payments.intent_consumed` whenever a transaction consumes an
intent. Subscribers receive the original reference, the transaction hash, and
any merchant/device annotations carried in the envelope.
