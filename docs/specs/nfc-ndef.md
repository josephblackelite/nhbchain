# NFC/NDEF carrier for NHB Pay intents (proposal, not implemented)

**Status:** this is a design proposal only. This repository has no CBOR
encoder or decoder, no NDEF record builder, and no registration of the
`application/nhbpay+cbor` media type. The URI it carries is the convention used
by `sdk/pos/examples/create_intent.go`; see [nhb-pay.md](./nhb-pay.md). Neither the
node nor any SDK code here reads or writes NDEF.

## Proposed record layout

An NDEF message with two records, in order:

1. Well-known type `U` (URI record) holding the `nhbpay://intent/...` URI, with
   URI identifier code `0x00` (no prefix) so the payload is the raw URI bytes.
2. MIME record `application/nhbpay+cbor` holding a CBOR map. A reader that cannot
   parse the CBOR record falls back to the URI record.

Both records would set the Short Record bit when the payload is under 255 bytes.

## Proposed CBOR map

| Key | Type | Value |
| --- | --- | --- |
| `intent_ref` | byte string | The intent reference bytes. |
| `intent_expiry` | unsigned integer | Expiry, unix seconds. |
| `merchant_addr` | text | Merchant address string. |
| `amount` | text | Decimal amount string, as in the URI. |
| `currency` | text | Currency code, as in the URI. |
| `paymaster` | text, optional | As in the URI. |
| `device_id` | text, optional | As in the URI. |
| `sig` | byte string, optional | Raw signature bytes (the URI carries the same signature hex-encoded in `sig`). |
| `ext` | map, optional | Vendor extensions; readers ignore unknown keys. |

The signature would be computed over the same canonical string as the URI
([nhb-pay.md](./nhb-pay.md#uri-convention-used-by-the-sdk-example)); a reader
comparing it with the URI parameter hex-encodes the CBOR bytes first.

`amount` and `currency` exist only in this off-chain carrier; the chain has no
such fields ([nhb-pay.md](./nhb-pay.md#on-chain-fields)).
