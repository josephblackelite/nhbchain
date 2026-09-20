# Swap price proofs and oracle guards

This page describes what the chain itself checks about prices when a swap
voucher is minted, and the node settings around it. Sources:
`native/swap/oracle_verify.go`, `native/swap/engine.go`,
`native/swap/oracle.go`, `core/swap_voucher_tx.go`.

## Price proof on `TxTypeSwapVoucherMint`

A voucher submission must carry a price proof. `PriceProofEngine.Verify` is
called with signatures always required (`RequireSignature(true)`), regardless of
the `Risk.PriceProofSignatureRequired` setting.

A proof (`swap.PriceProof`) has `Domain`, `Provider`, `Pair` (`BASE/QUOTE`),
`Rate`, `Timestamp` and a 65-byte `Signature`. The signed message is:

```
NHB_SWAP_PRICE_V1|provider=<lowercase provider>|pair=<BASE>/<QUOTE>|rate=<rate with 18 decimals>|ts=<unix seconds>
```

The digest is keccak256 of that string, and the proof id (`priceProofId`) is its
hex encoding. `Verify` rejects a proof when:

| Check | Error |
| --- | --- |
| `Domain` is not `NHB_SWAP_PRICE_V1` | `swap: price proof domain invalid` |
| `Provider` is empty or differs from the voucher's provider | `swap: price proof provider mismatch` |
| Base is not `NHB` or `ZNHB`, quote is not `USD`, or base differs from the voucher's token | `swap: price proof pair invalid` |
| No signature | `swap: price proof signature missing` |
| No signer registered for the provider | `swap: price proof signer unknown` |
| Signature is not 65 bytes, or recovers to an address other than the registered signer | `swap: price proof signature invalid` |
| Timestamp is more than 30 seconds in the future, or older than `MaxQuoteAgeSeconds` | `swap: price proof stale` |
| Rate differs from the last recorded proof for the same base by more than `PriceProofMaxDeviationBps` | `swap: price proof deviation too large` |

The transaction handler maps these to `ErrSwapPriceProofRequired`,
`ErrSwapPriceProofInvalid`, `ErrSwapPriceProofSignerUnknown`,
`ErrSwapPriceProofStale` and `ErrSwapPriceProofDeviation`
(`core/swap.go`). The accepted proof's rate and timestamp are recorded for the
next deviation check.

The signer for a provider is set by the `policy.swapPriceSigner` governance
proposal ([proposal types](../gov/proposal-types.md)).

`applySwapVoucherMintTransaction` (`core/swap_voucher_tx.go`) applies two
separate `SlippageBps` checks, and a voucher must pass both, each failing with
`ErrSwapSlippageExceeded`:

1. **Proof rate.** `swap.ComputeMintAmount` divides the voucher's fiat amount by
   the price proof's rate (USD per token) and scales by the token's decimals.
   The voucher's ZNHB `Amount` must be within `SlippageBps` of that result.
2. **Curve cost.** The Genesis Treasury Distribution Curve prices the ZNHB
   `Amount` against the Sale Pool's current cumulative sold counter
   ([tokenomics](../tokenomics/tokenomics.md#4-the-genesis-treasury-distribution-curve)).
   The voucher's USD amount must be within `SlippageBps` of that curve cost.

The curve is the authoritative treasury price and the Sale Pool is what the
mint draws from (code comment at `core/swap_voucher_tx.go`, above the curve
check).

## Node settings (`[swap]`)

`swap.Config.Normalise` (`native/swap/oracle.go`) applies these defaults when a
value is unset:

| Key | Default |
| --- | --- |
| `AllowedFiat` | `["USD"]` |
| `MaxQuoteAgeSeconds` | `120` |
| `SlippageBps` | `50` |
| `OraclePriority` | `["manual"]` |
| `TwapWindowSeconds` | `0` (negative values become `0`) |
| `TwapSampleCap` | `128` |
| `PriceProofMaxDeviationBps` | `100` (also used when set to `0`) |
| `PayoutAuthorities` | `["treasury"]` |

## Manual quote

`swap_setManualQuote` (authenticated JSON-RPC) publishes a rate into the node's
manual oracle (`Node.SetSwapManualQuote`). Params:
`[{"base": "...", "quote": "...", "rate": "<decimal>", "timestamp": <unix, optional>}]`;
the result is `{ok, base, quote, rate, observedAt}`. It fails with `swap: manual
oracle not configured` when no manual oracle exists. Without a fresh call the
manual tier goes stale after `MaxQuoteAgeSeconds`.

## Events

`swap.mint.proof` (`core/events/swap.go`) is emitted with a voucher mint and
carries, when present: `providerTxId`, `orderId`, `token`, `priceProofId`,
`source`, `oracleMedian`, `oracleFeeders` (comma-separated), `quoteTs`,
`twapRate`, `twapObservations`, `twapWindowSeconds`, `twapStart`, `twapEnd`.
`swap.redeem.proof` is emitted from `core/node.go` for redeemed vouchers.

## Redeem side

The swap-out burn (`TxTypeRedeemNHB`) is limited by the governed caps set with
`policy.swapRiskParams` (read back with the public `swap_getRiskParams` RPC).
`policy.redemptionFeeParams` stores a redemption fee rate, floor and cap (read
back with `swap_getRedemptionFeeParams`); `applyRedeemNHB` burns the full
requested NHB amount and does not deduct that fee itself. Defaults are listed in
[params](../governance/params.md#keys-written-by-dedicated-proposal-kinds).
