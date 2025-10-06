# NHB Pay Point-of-Sale Intents

This document defines the portable payment intent payloads that NHB Pay POS
terminals emit and compatible wallets consume. The same fields are attached to
every on-chain `TxEnvelope` and signed along with the transaction body so that
validators can reject replayed payloads and enforce bounded settlement windows.

## Envelope metadata

| Field | Type | Description |
| ----- | ---- | ----------- |
| `intent_ref` | 32&nbsp;bytes | Merchant generated nonce that uniquely identifies the payment intent. Terminals SHOULD generate a cryptographically random 32 byte value. Wallets and services MUST treat the field as opaque bytes on-chain. |
| `intent_expiry` | `uint64` seconds | Unix timestamp indicating the absolute expiry of the intent. Validators clamp the stored expiry to `min(intent_expiry, block_time + 24h)` and reject any payload where `now >= intent_expiry`. |
| `merchant_addr` | string | Canonical merchant address presented to the customer. The canonical representation is the NHB bech32 string (e.g. `nhb1...`). The consensus layer stores the trimmed string exactly as supplied. |
| `amount` | string | Decimal amount in NHB minor units (for example `12.34`). The format mirrors API amounts: a base 10 string with up to 8 fractional digits. |
| `currency` | string | ISO-4217 currency code associated with the amount (e.g. `USD`). |
| `paymaster` | string (optional) | Address or identifier of the entity subsidising the payment. Wallets MUST ignore unknown paymaster schemes but SHOULD preserve the value when submitting transactions. |
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
nhbpay://intent/<intent_ref_hex>?amount=<decimal>&currency=<code>&expiry=<unix_seconds>&merchant=<bech32>[&paymaster=<scheme:value>][&device=<url-encoded>][&sig=<sig_hex>]
```

* `<intent_ref_hex>` is the lowercase hex encoding of the 32 byte reference.
* `amount` is the decimal string amount including fractional digits.
* `currency` is the three letter ISO-4217 currency code.
* `<unix_seconds>` is the decimal representation of `intent_expiry`.
* `<bech32>` is the canonical merchant address.
* `paymaster` and `device` parameters are optional; their values MUST be URL
  encoded. Unknown query parameters MUST be ignored by wallets.
* `sig` is the lowercase hex encoding of the canonical signature described
  below. Wallets MAY reject unsigned payloads depending on policy.

#### Canonical string-to-sign

Terminals MUST derive the signature payload by concatenating the URI scheme and
a sorted key/value list. The canonical plaintext is:

```
nhbpay://intent/<intent_ref_hex>?amount=<amount>&currency=<currency>&expiry=<expiry>&merchant=<merchant>[&device=<device>][&paymaster=<paymaster>]
```

* Optional parameters are included only when present in the original intent.
* Keys are lowercase ASCII and MUST be appended in the order shown above.
* Values MUST be percent decoded prior to signing.
* The signature is generated with the merchant private key over the UTF-8 bytes
  of the canonical string. Ed25519 is the default POS key type.

The resulting signature is hex encoded and placed in the `sig` query parameter
of the URI. Wallets MUST verify the signature before constructing and
submitting the payment transaction.

##### Worked example

Given the following parameters:

| Field | Value |
| ----- | ----- |
| `intent_ref` | `0x2d8c7fd3e1a94f4c998e4cfedc3a4567bb12aa09887766554433221100ff9a01` |
| `amount` | `15.25` |
| `currency` | `USD` |
| `merchant_addr` | `nhb1m0ckmerchantaddre55` |
| `intent_expiry` | `1707436800` |
| `device_id` | `kiosk-7` |
| `paymaster` | `nhb1sponsorship` |

The canonical string-to-sign is:

```
nhbpay://intent/2d8c7fd3e1a94f4c998e4cfedc3a4567bb12aa09887766554433221100ff9a01?amount=15.25&currency=USD&expiry=1707436800&merchant=nhb1m0ckmerchantaddre55&device=kiosk-7&paymaster=nhb1sponsorship
```

Assuming the merchant signs this string with Ed25519 and obtains the signature
`0x5b0481e43cbb27c4c76bf0fa104d8a2ffb329a84797d0c0edc55fb6a2dcef0125c7d4090560ce10a4bf845ba1b4c745cf3e5012ef0d8c2a8d98d00ab91c5dd1a`, the full URI becomes:

```
nhbpay://intent/2d8c7fd3e1a94f4c998e4cfedc3a4567bb12aa09887766554433221100ff9a01?amount=15.25&currency=USD&expiry=1707436800&merchant=nhb1m0ckmerchantaddre55&device=kiosk-7&paymaster=nhb1sponsorship&sig=5b0481e43cbb27c4c76bf0fa104d8a2ffb329a84797d0c0edc55fb6a2dcef0125c7d4090560ce10a4bf845ba1b4c745cf3e5012ef0d8c2a8d98d00ab91c5dd1a
```

Wallets display or embed this URI in QR codes and deep links.

### NFC payloads

When transporting an intent over NFC, use the dedicated NDEF layouts described
in [NHB Pay NFC intents](./nfc-ndef.md). Wallets MUST follow the signing
instructions above prior to validating the payload carried in the NDEF record.

## Events

The chain emits `payments.intent_consumed` whenever a transaction consumes an
intent. Subscribers receive the original reference, the transaction hash, and
any merchant/device annotations carried in the envelope.
