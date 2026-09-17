# Swap Voucher RPC Summary

## Voucher Schema

The node accepts version 1 swap vouchers with the domain string `NHB_SWAP_VOUCHER_V1`. Each voucher serialises to JSON with the following fields:

| Field | Type | Description |
|-------|------|-------------|
| `domain` | string | Must equal `NHB_SWAP_VOUCHER_V1`. Protects against cross-protocol reuse. |
| `chainId` | integer | Chain identifier derived from the NHB genesis hash. |
| `token` | string | Supported token symbol. The initial deployment only permits `ZNHB`. |
| `recipient` | string | NHB bech32 address that receives the minted ZNHB. |
| `amount` | string | Positive decimal string representing the amount of ZNHB (wei precision). |
| `fiat` | string | Fiat currency code supplied by the gateway (e.g. `USD`). |
| `fiatAmount` | string | Fiat amount paid by the customer. |
| `rate` | string | Fiat→token conversion rate embedded in the voucher. |
| `orderId` | string | Gateway order identifier used as the replay-protection nonce. |
| `nonce` | string | Hex-encoded random salt (case-insensitive) included in the signed payload. |
| `expiry` | integer | Unix timestamp after which the voucher is rejected. |

The keccak256 hash is computed over the deterministic template

```
NHB_SWAP_VOUCHER_V1|chain=<chainId>|token=<token>|to=<recipient_hex>|amount=<amount>|fiat=<fiat>|fiatAmt=<fiatAmount>|rate=<rate>|order=<orderId>|nonce=<nonce>|exp=<expiry>
```

where `recipient_hex` is the 20-byte NHB address in lowercase hex and `nonce` is lowercase hex without the `0x` prefix.

## RPC Endpoint

`swap_submitVoucher` accepts a single parameter object:

```json
{
  "voucher": { /* VoucherV1 payload */ },
  "sig": "0x<secp256k1 signature>"
}
```

On success the response contains `{ "txHash": "0x...", "minted": true }`. Failures return standard JSON-RPC errors:

- invalid domain, chain, token, expiry or signature → `codeInvalidParams`
- unauthorized signer → `codeUnauthorized`
- duplicate `orderId` replay → `codeDuplicateTx`

## Security Notes

- The node compares `domain` with `NHB_SWAP_VOUCHER_V1` and verifies `chainId` against the running chain.
- Vouchers expire when `now > expiry`; clients should refresh vouchers frequently (default gateway TTL: 15 minutes).
- Only `ZNHB` minting is enabled for MVP and the recovered signer must match the configured `ZNHB` mint authority.
- Replays are prevented by persisting `orderId` inside the state trie; duplicate submissions receive `codeDuplicateTx`.
- Successful mints append a `swap.minted` event with `{orderId, recipient, amount, fiat, fiatAmount, rate}` for downstream indexing and compliance monitoring.
