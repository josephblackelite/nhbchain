# Swap Oracle Price Proofs

Every `TxTypeSwapVoucherMint` must carry a signed price proof. The chain
verifies it deterministically against on-chain state; it does not call any live
oracle while executing a block (`applySwapVoucherMintTransaction` in
`core/swap_voucher_tx.go`).

## Payload format

| Field | Description |
|--------------|-----------------------------------------------------------------------------|
| `domain` | Must equal `NHB_SWAP_PRICE_V1` (compared case-insensitively). |
| `provider` | Provider identifier. Must match the voucher submission's `provider` (case-insensitive). |
| `pair` | `BASE/QUOTE`. The verifier accepts base `NHB` or `ZNHB` with quote `USD`, and the base must equal the voucher token. Because vouchers must be `ZNHB`, only `ZNHB/USD` succeeds on the mint path. |
| `rate` | Positive decimal string, USD per token. |
| `timestamp` | Positive Unix seconds. |
| `signature` | 65-byte signature. Mandatory on the mint path. |

The signed message is

```
NHB_SWAP_PRICE_V1|provider=<provider>|pair=<BASE>/<QUOTE>|rate=<rate>|ts=<timestamp>
```

`<provider>` is lower-cased, `<BASE>`/`<QUOTE>` upper-cased, `<rate>` is
rendered with exactly 18 decimal places, and `<timestamp>` is Unix seconds.
The digest is `keccak256` of that string (`PriceProof.CanonicalMessage`,
`Hash` in `native/swap/oracle_verify.go`). The proof ID stored on the voucher
(`priceProofId`) is the hex of that digest.

## Validation

`PriceProofEngine.Verify` (`native/swap/engine.go`) runs these checks, in this
order:

1. **Domain** - `ErrPriceProofDomain`.
2. **Provider** - proof provider must be non-empty and match the submission
   provider (`ErrPriceProofProviderMismatch`).
3. **Pair** - base `NHB`/`ZNHB`, quote `USD`, base equal to the token
   (`ErrPriceProofPair`).
4. **Signature** - the mint path forces `RequireSignature(true)`, regardless
   of `swap.risk.PriceProofSignatureRequired`. The signer registered for the
   lower-cased provider is looked up in state (`ErrPriceProofSignerUnknown` if
   none); the signature must be 65 bytes and recover to that address
   (`ErrPriceProofSignatureInvalid`).
5. **Freshness** - a timestamp more than 30 seconds after block time, or older
   than `MaxQuoteAgeSeconds`, fails with `ErrPriceProofStale`. Time is the
   block timestamp.
6. **Deviation** - if `PriceProofMaxDeviationBps > 0` and a previous proof
   exists for the same base, `|rate - previous| > previous * bps / 10000`
   fails with `ErrPriceProofDeviation`.

After the price proof passes, the mint path also runs the provider allow-list,
sanctions, risk limits, mint-authority signature, slippage, duplicate and
Sale Pool checks. Only when those pass does it call
`PriceProofEngine.Record`, which stores the proof as the last accepted proof
for that base at `swap/oracle/last/<BASE>` (`applySwapVoucherMintTransaction`). The voucher record stores the proof ID as `priceProofId` and the
proof timestamp as `quoteTs`; `source` is the lower-cased proof provider.

The mint-amount check derives the expected amount from the proof rate:
`fiatAmount / rate * 10^decimals`, and requires the voucher `amount` to be
within `SlippageBps` of it (`swap.ComputeMintAmount`,
`applySwapVoucherMintTransaction`). There is no comparison against a
live oracle rate on this path.

### Error mapping in the mint transaction

| Verifier error | Chain error (message) |
| --- | --- |
| nil proof, missing signature | `ErrSwapPriceProofRequired` (`swap: price proof required`) |
| domain, pair, provider mismatch, invalid signature | `ErrSwapPriceProofInvalid` (`swap: invalid price proof`) |
| unknown signer | `ErrSwapPriceProofSignerUnknown` (`swap: price proof signer unknown`) |
| stale (including future-dated) | `ErrSwapPriceProofStale` (`swap: price proof stale`) |
| deviation | `ErrSwapPriceProofDeviation` (`swap: price proof deviation too large`) |

`rpc/swap_handlers.go` returns all of these as HTTP 400 / `codeInvalidParams`.

## Signer management

Signers are stored at `swap/oracle/signer/<lower-case provider>`. They are
registered or revoked by a governance proposal of kind `policy.swapPriceSigner`
with payload `{"provider": "...", "signerAddress": "...", "memo": "...",
"revoke": false}` (`native/governance/types.go` `SwapPriceSignerPayload`,
`native/governance/engine.go` `parseSwapPriceSignerPayload`). `provider` is
required and at most 64 characters; `signerAddress` is required unless
`revoke` is true and must not be the zero address.

## Configuration

`[swap]` keys that feed this path (`native/swap/oracle.go` `Config.Normalise`):

| Key | Effect | Value when unset or `0` |
| --- | --- | --- |
| `MaxQuoteAgeSeconds` | Maximum proof age | `120` |
| `PriceProofMaxDeviationBps` | Maximum rate move between consecutive accepted proofs | `100` |
| `SlippageBps` | Maximum voucher-amount deviation from the computed amount | `50` |

`Normalise` replaces `0` with the default for all three, so
`PriceProofMaxDeviationBps` cannot be set to `0` to disable the deviation
check. The repository `config.toml` sets `PriceProofMaxDeviationBps = 0`,
which therefore resolves to `100`.
