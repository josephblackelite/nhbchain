# POS intent field reference

This reference enumerates every field emitted by NHB Pay point-of-sale devices.
Use it alongside the canonical wire formats in [NHB Pay point-of-sale intents](./nhb-pay.md)
and the real-time QoS guarantees in [POS quality of service](./pos-qos.md).

## Core envelope metadata

| Field | Type | Description |
| ----- | ---- | ----------- |
| `intent_ref` | 32&nbsp;bytes | Merchant generated nonce that uniquely identifies the payment intent. Values MUST be cryptographically random and collision resistant. Validators persist the reference to guarantee single use. |
| `intent_expiry` | `uint64` seconds | Unix timestamp indicating the absolute expiry. Consensus clamps the stored expiry to a 24 hour TTL and rejects any payload where `now >= intent_expiry`. |
| `merchant_addr` | string | Canonical merchant address that wallets surface to the payer. The consensus layer stores the supplied bech32 address verbatim. |
| `amount` | string | Decimal amount encoded with up to eight fractional digits (for example `12.34`). The ledger interprets the string as base 10 NHB minor units. |
| `currency` | string | ISO-4217 currency code that qualifies the amount (for example `USD`). |

## Optional context fields

| Field | Type | Description |
| ----- | ---- | ----------- |
| `paymaster` | string | Identifier of the entity subsidising the payment. Wallets MUST preserve unknown schemes when relaying the payload and MAY render a human friendly label when the scheme is recognised. |
| `device_id` | string | Opaque identifier emitted by the terminal. Consensus exposes the value through events so operators can correlate payments with hardware inventory. |

## Derived representations

| Representation | Fields | Notes |
| -------------- | ------ | ----- |
| Deep-link / QR URI | All core fields, plus optional context | `intent_ref`, `amount`, `currency`, `expiry`, and `merchant` populate the mandatory query parameters. Optional `paymaster`, `device`, and `sig` entries follow the canonical ordering described in [NHB Pay point-of-sale intents](./nhb-pay.md#canonical-encodings). |
| NFC NDEF payload | `intent_ref`, `intent_expiry`, optional `amount` and `currency` | Use the records defined in [NHB Pay NFC intents](./nfc-ndef.md). Wallets MUST verify the same signature and expiry semantics before accepting a tap-to-pay payload. |

## Event surfaces

Transactions that consume an intent emit the `payments.intent_consumed` event
with the original `intent_ref`, the finalising transaction hash, and any optional
fields preserved on-chain. Consumers can use the metadata to reconcile tap-to-pay
transactions with settlement events described in [POS lifecycle events](./pos-lifecycle.md).
