# NHB Pay NFC intents

This document describes the NFC Forum NDEF payloads that terminals MUST produce
when broadcasting NHB Pay intents over tap-to-pay experiences. The layout keeps
compatibility with existing wallet stacks by including both a human-readable URI
record and a binary CBOR record for lossless metadata transport.

## Record layout

Terminals MUST emit an NDEF message containing the following records in order:

1. **Well-known type `U` (URI record).** Contains the NHB Pay deep link defined
   in [NHB Pay Point-of-Sale Intents](./nhb-pay.md). This allows wallets that
   only understand URI records to still complete the payment flow.
2. **MIME type `application/nhbpay+cbor`.** Contains the CBOR representation of
   the canonical fields. Wallets SHOULD prefer this record to avoid ambiguity
   introduced by URI encoding.

Both records are flagged with the Short Record (SR) bit when the payload length
is below 255 bytes. The URI identifier code MUST be set to `0x00` (no prefix)
so that the payload stores the raw URI bytes.

## CBOR payload

The CBOR record encodes a map with the following keys:

| Key | Type | Description |
| --- | ---- | ----------- |
| `intent_ref` | `bstr` | 32 byte reference copied verbatim from the envelope. |
| `intent_expiry` | `uint` | Expiry timestamp in Unix seconds. |
| `merchant_addr` | `tstr` | Canonical bech32 merchant address. |
| `amount` | `tstr` | Decimal amount string, identical to the URI. |
| `currency` | `tstr` | ISO-4217 currency code. |
| `paymaster` | `tstr` (optional) | Paymaster identifier. |
| `device_id` | `tstr` (optional) | POS device identifier. |
| `sig` | `bstr` (optional) | Merchant signature bytes. Present when the URI also carries the `sig` parameter. |

Terminals MAY include vendor-specific metadata inside a nested map under the key
`ext`. Wallets MUST ignore unknown keys.

### Signing bytes

Terminals MUST derive the signature from the canonical string described in the
[NHB Pay POS URI specification](./nhb-pay.md#canonical-string-to-sign). When
present, the signature bytes stored in the CBOR payload are identical to the
raw Ed25519 signature (not hex encoded). Wallets verifying a CBOR payload MUST
hex encode the signature before comparing it with the URI query parameter, if
present.

### Example

The example below shows a diagnostic hexdump of a CBOR record with all fields
populated. Whitespace has been added for clarity.

```
A8                                      # map(8)
  6A                                    # text(10)
    696E74656E745F726566                # "intent_ref"
  50                                    # bytes(16)
    2D8C7FD3E1A94F4C998E4CFEDC3A4567
  6D                                    # text(13)
    696E74656E745F657870697279          # "intent_expiry"
  1A 659D5B00                           # unsigned(1707436800)
  6D                                    # text(13)
    6D65726368616E745F61646472          # "merchant_addr"
  78 18                                 # text(24)
    6E6862316D30636B6D65726368616E7461646472653535
  66                                    # text(6)
    616D6F756E74                        # "amount"
  65                                    # text(5)
    31352E3235                          # "15.25"
  68                                    # text(8)
    63757272656E6379                    # "currency"
  63                                    # text(3)
    555344                              # "USD"
  69                                    # text(9)
    7061796D6173746572                  # "paymaster"
  73                                    # text(19)
    6E68623173706F6E736F7273686970      # "nhb1sponsorship"
  69                                    # text(9)
    6465766963655F6964                  # "device_id"
  67                                    # text(7)
    6B696F736B2D37                      # "kiosk-7"
  63                                    # text(3)
    736967                              # "sig"
  58 40                                 # bytes(64)
    5B0481E43CBB27C4C76BF0FA104D8A2FFB329A84797D0C0EDC55FB6A2DCEF012
    5C7D4090560CE10A4BF845BA1B4C745CF3E5012EF0D8C2A8D98D00AB91C5DD1A
```

Wallets that cannot parse the CBOR payload SHOULD fall back to the URI record.
