# Swap Oracle Price Proofs

Swap voucher mints now require a signed price proof that anchors the USD conversion rate for NHB/ZNHB. The proof couples the provider identifier, currency pair, observed price, and timestamp with a deterministic message hash that is verified on-chain prior to minting.

## Payload format

A price proof contains the following fields:

| Field        | Description                                                                 |
|--------------|-----------------------------------------------------------------------------|
| `domain`     | Must equal `NHB_SWAP_PRICE_V1`.                                             |
| `provider`   | Lower-case provider identifier registered in the signer allow-list.         |
| `pair`       | Canonical `BASE/QUOTE` string. Only `NHB/USD` and `ZNHB/USD` are accepted.  |
| `rate`       | USD price rendered as a decimal string (18 decimal precision recommended).  |
| `timestamp`  | Unix timestamp (seconds) in UTC when the price was observed.                |
| `signature`  | 65-byte secp256k1 signature over the canonical message (see below).         |

The canonical message that is signed is rendered as:

```
NHB_SWAP_PRICE_V1|provider=<provider>|pair=<BASE>/<QUOTE>|rate=<rate>|ts=<timestamp>
```

`<provider>` is lower-cased, `<BASE>`/`<QUOTE>` are upper-cased, `<rate>` is normalised with 18 decimal places, and `<timestamp>` is the Unix seconds value. The on-chain verifier recomputes this payload, derives the keccak256 hash, and recovers the signer using the supplied signature.

## Validation pipeline

During `swap_submitVoucher` the node performs the following checks before minting:

1. **Signer allow-list** – the recovered signer address must match the provider signer stored in state (`swap/oracle/signer/{provider}`). Unknown providers are rejected.
2. **Domain & pair guards** – the proof must use the `NHB_SWAP_PRICE_V1` domain and the base token must be either `NHB` or `ZNHB` with `USD` as the quote.
3. **Freshness** – proofs older than `swap.MaxQuoteAgeSeconds` or more than 30 seconds in the future are rejected (`swap.ErrPriceProofStale`).
4. **Deviation** – the new rate may not deviate from the previous stored proof by more than `swap.PriceProofMaxDeviationBps` basis points (`swap.ErrPriceProofDeviation`). The last accepted proof is persisted under `swap/oracle/last/{base}`.
5. **Oracle parity** – the configured price oracle is still queried; the returned rate must match the signed proof within the configured deviation tolerance. This prevents replaying stale proofs while the live oracle disagrees.

Only after all validation steps succeed does the node record the proof and mint tokens. The voucher ledger stores the proof hash (`priceProofId`) and the proof timestamp as the canonical quote time.

## Signer management

Signer addresses are stored in consensus state via the `swap/oracle/signer/{provider}` key. Governance tooling must update this mapping whenever a provider rotates keys. The helper API `SwapSetPriceSigner` in the state manager simplifies integration tests and tooling.

## Configuration

`swap.PriceProofMaxDeviationBps` controls the allowed basis-point difference between consecutive proofs and between the proof and the live oracle rate. The default is `100` (1%). Setting the value to `0` disables deviation enforcement, although this is not recommended for production environments.

## Failure modes

The RPC service maps the new validation errors to user-facing responses:

- Missing or malformed proofs → `invalid params` with `swap: price proof required/invalid`.
- Unknown signer → `invalid params` with `swap: price proof signer unknown`.
- Stale proofs → `invalid params` with `swap: price proof stale`.
- Deviation breaches → `invalid params` with `swap: price proof deviation too large`.

Clients should log and alert on these failures because they may indicate compromised providers or upstream oracle drift.
