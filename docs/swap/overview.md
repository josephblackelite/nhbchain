# Swap Voucher RPC Summary

`swap_submitVoucher` accepts an off-chain-signed voucher, wraps it in a
`TxTypeSwapVoucherMint` (`0x1E`) transaction, and puts that transaction in the
mempool. It does not wait for a block. The chain later applies the
transaction deterministically on every validator
(`core/swap_voucher_tx.go` `applySwapVoucherMintTransaction`).

The asset delivered is **ZNHB moved out of the admin wallet and the treasury
Sale Pool**, not newly minted supply (`core/swap_voucher_tx.go` lines 513-555).

## Voucher schema (V1)

`swap_submitVoucher` decodes the `voucher` field as `swap.VoucherV1`
(`native/swap/voucher.go`). JSON fields:

| Field | Type | Rule |
|-------|------|------|
| `domain` | string | Must equal `NHB_SWAP_VOUCHER_V1` (`ErrSwapInvalidDomain` otherwise). |
| `chainId` | integer | Must equal the node's chain ID (`ErrSwapInvalidChainID`). |
| `token` | string | Upper-cased on decode. Must be `ZNHB` (`ErrSwapInvalidToken`). |
| `recipient` | string | NHB bech32 address. |
| `amount` | string | Positive base-10 integer, wei of ZNHB. |
| `fiat` | string | Must be in `[swap].AllowedFiat` (`ErrSwapUnsupportedFiat`). |
| `fiatAmount` | string | Decimal string; used to compute the expected mint amount. |
| `rate` | string | Free-form string carried into the `swap.minted` event. The mint amount is computed from the price proof rate, not from this field. |
| `orderId` | string | Required. Replay-protection nonce (see below). |
| `nonce` | string | Required hex string; a `0x` prefix and upper case are accepted on input. |
| `expiry` | integer | Unix seconds. Rejected when `expiry <= block time` (`ErrSwapExpired`). |

The signed digest is `keccak256` of

```
NHB_SWAP_VOUCHER_V1|chain=<chainId>|token=<token>|to=<recipient_hex>|amount=<amount>|fiat=<fiat>|fiatAmt=<fiatAmount>|rate=<rate>|order=<orderId>|nonce=<nonce>|exp=<expiry>
```

where `recipient_hex` is the 20-byte address as lower-case hex without `0x` and
`nonce` is lower-case hex without `0x` (`VoucherV1.Hash`). The signature is 65
bytes and is recovered with go-ethereum `SigToPub`; the recovered address must
equal the mint authority stored for the `ZNHB` token (`ErrSwapInvalidSigner`).
The provider and `providerTxId` are **not** covered by a V1 signature.

### V2 vouchers

`swap.VoucherV2` (domain `NHB_SWAP_VOUCHER_V2`) extends the V1 template with
`|provider=<provider>|providerTxId=<providerTxId>`, so a V2 signature binds
those two fields (`VoucherV2.Hash`). The transaction decoder accepts a
`voucherV2` payload, and for V2 it always takes provider and providerTxId from
the signed voucher. The `swap_submitVoucher` RPC handler only builds V1
submissions (`Node.SwapSubmitVoucher` requires `submission.Voucher`).

## RPC

`swap_submitVoucher` takes one parameter object (`rpc/swap_handlers.go`):

```json
{
  "voucher": { "...": "VoucherV1 fields above" },
  "sig": "0x<65-byte hex signature>",
  "provider": "provider-id",
  "providerTxId": "provider-transaction-id",
  "username": "optional",
  "address": "optional",
  "usdAmount": "optional decimal string",
  "priceProof": {
    "domain": "NHB_SWAP_PRICE_V1",
    "provider": "provider-id",
    "pair": "ZNHB/USD",
    "rate": "0.05",
    "timestamp": 1734000000,
    "signature": "0x<65-byte hex signature>"
  }
}
```

* `voucher`, `sig`, `provider` and `providerTxId` are required.
* `priceProof` is required by the chain (`ErrSwapPriceProofRequired`). See
  [oracle-verification.md](oracle-verification.md).
* `usdAmount` defaults to `voucher.fiatAmount` when `fiat` is `USD`. For any
  other fiat it must be supplied: the chain parses it as the USD budget for the
  Sale Pool curve check and rejects the voucher when it is not a positive
  decimal (`core/swap_voucher_tx.go` lines 468-504).

On success the result is `{"txHash": "0x...", "minted": false}`. `minted` is
always `false` because the transaction is only enqueued
(`Node.SwapSubmitVoucher`, `core/node.go` line 7738). Poll `swap_voucher_get`
until the record's `status` is `minted`.

Errors (`rpc/swap_handlers.go`):

| Condition | HTTP status | JSON-RPC code |
| --- | --- | --- |
| Invalid signer, provider not allowed, sanctions failure | 401 / 403 / 403 | `codeUnauthorized` |
| Below minimum, above maximum, daily or monthly cap exceeded | 400 | `codeInvalidParams` |
| Velocity limit exceeded | 429 | `codeRateLimited` |
| `orderId` already used, or `providerTxId` already recorded | 409 | `codeDuplicateTx` (data: the `orderId`) |
| Invalid domain, chain ID, expiry, token, signature; any price-proof error; mint paused; unsupported fiat; oracle unavailable (`ErrSwapOracleUnavailable`); stale quote (`ErrSwapQuoteStale`); slippage exceeded | 400 | `codeInvalidParams` |
| Anything else | 500 | `codeServerError` |

`ErrSwapProviderTxIDCollision` (`swap: providerTxId collides with a different
order`) is not in the handler's switch and therefore surfaces as a 500
`swap voucher failed`.

## Read methods

* `swap_voucher_get` - param: the `providerTxId` string (or `{"providerTxId": "..."}`).
* `swap_voucher_list` - params: `startTs`, `endTs`, optional `cursor`, optional `limit` (default 50). Returns `{"vouchers": [...], "nextCursor": "..."}`.
* `swap_voucher_export` - params: `startTs`, `endTs`. Returns `{"csvBase64", "count", "totalMintWei"}`.

A voucher record contains: `provider`, `providerTxId`, `fiatCurrency`,
`fiatAmount`, `usd`, `rate`, `token`, `mintAmountWei`, `username`, `address`,
`quoteTs`, `source`, `minterSig`, `status`, `createdAt`, and when present
`priceProofId`, `oracleMedian`, `oracleFeeders`, `recipient`. It also always
contains `twapObservations` and `twapWindowSeconds`, and `twapRate`,
`twapStart`, `twapEnd` when set. The mint transaction does not populate any
TWAP field, so those are `0` or absent on records it creates.

## Replay protection and events

* `orderId` is recorded as a used nonce in state on success (`ErrSwapNonceUsed`,
  message `swap: order already processed`).
* `providerTxId` is the ledger key. Re-submitting the same order returns
  `swap: provider transaction already processed`.
* A successful mint appends `swap.minted` with `orderId`, `recipient`,
  `amount`, `fiat`, `fiatAmount`, `rate`, and `swap.mint.proof` with
  `providerTxId`, `orderId`, `token`, `priceProofId`, `source`, `quoteTs`
  (`core/events/swap.go`).
* The mint is subject to the `swap` module pause (`Pauses.Swap`) and to the
  token's `MintPaused` flag (`ErrSwapMintPaused`).
