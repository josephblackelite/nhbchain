# Swap Risk Controls

Limits applied to voucher mints (`TxTypeSwapVoucherMint`) and to NHB
redemptions (`TxTypeRedeemNHB`). Mint-side limits come from the node's local
`config.toml`; redeem-side limits come from on-chain governance state.

## Mint-side configuration

`config.toml` sections `[swap]`, `[swap.risk]`, `[swap.providers]`,
`[swap.sanctions]` (`native/swap/oracle.go` `Config`, `native/swap/risk.go`
`RiskConfig`). Values below are the ones in the repository's `config.toml`:

```toml
[swap]
  AllowedFiat = ["USD", "EUR", "GBP"]
  MaxQuoteAgeSeconds = 120
  SlippageBps = 50
  OraclePriority = ["nowpayments", "coingecko", "manual"]
  TwapWindowSeconds = 300
  TwapSampleCap = 128
  PriceProofMaxDeviationBps = 0
  PayoutAuthorities = ["treasury"]
  [swap.risk]
    PerAddressDailyCapWei = "10000e18"
    PerAddressMonthlyCapWei = "300000e18"
    PerTxMinWei = "1e18"
    PerTxMaxWei = "50000e18"
    VelocityWindowSeconds = 600
    VelocityMaxMints = 5
    SanctionsCheckEnabled = true
    PriceProofSignatureRequired = false
    [swap.risk.cashOut]
  [swap.providers]
    Allow = ["nowpayments"]
  [swap.sanctions]
```

| Key | Behaviour |
| --- | --- |
| `AllowedFiat` | Voucher `fiat` must be listed (default `["USD"]`). `ErrSwapUnsupportedFiat`. |
| `MaxQuoteAgeSeconds` | Maximum age of the signed price proof. `<= 0` becomes `120`. |
| `SlippageBps` | Maximum deviation of the voucher amount from the amount computed from the proof rate, and of the Sale Pool curve cost from the USD budget. `0` becomes `50`. |
| `PriceProofMaxDeviationBps` | Maximum move between consecutive accepted proofs. `0` becomes `100`; it cannot be disabled. |
| `PerTxMinWei` / `PerTxMaxWei` | Reject vouchers below / above. Empty or `0` disables that check. |
| `PerAddressDailyCapWei` / `PerAddressMonthlyCapWei` | Recipient's mint total for the current UTC day (`YYYY-MM-DD`) / UTC month (`YYYY-MM`), including this voucher, must not exceed the cap. Empty or `0` disables. |
| `VelocityWindowSeconds` + `VelocityMaxMints` | Blocks a mint when the recipient already has `VelocityMaxMints` or more mints in the last `VelocityWindowSeconds`. Both must be > 0 to be enforced. |
| `SanctionsCheckEnabled` | When `true`, the sanctions checker built from `[swap.sanctions].DenyList` runs. |
| `PriceProofSignatureRequired` | Legacy knob. The mint transaction always requires a signed proof. |
| `[swap.providers].Allow` | Lower-cased allow list of provider IDs. **Empty list allows every provider.** |
| `[swap.sanctions].DenyList` | Bech32 addresses. Recipients on the list fail the sanctions check. With an empty list, all addresses pass. |
| `[swap.risk.cashOut]` | `AssetCaps` / `Tiers` are parsed and validated but no code path reads them (see below). |

Amount values accept decimal integers, scientific notation such as `10000e18`,
and `_` separators. The result must be a non-negative integer
(`parseWeiAmount`).

Config is read at node start (`cmd/nhb/main.go` `node.SetSwapConfig`). Restart
the node to apply a change. Because every validator executes mint
transactions against its **own** local config, validators must be configured
identically.

## Order of checks in the mint transaction

`applySwapVoucherMintTransaction` (`core/swap_voucher_tx.go`):

1. Module pause (`Pauses.Swap`).
2. Voucher shape: domain, chain ID, expiry, amount, nonce, recipient,
   `orderId`, provider, `providerTxId`, 65-byte signature, token `ZNHB`.
3. Fiat allow-list.
4. Price proof verification ([oracle-verification.md](oracle-verification.md)).
5. Provider allow-list (only when `Allow` is non-empty) - emits
   `swap.alert.limit_hit` with `limit=provider`, returns
   `ErrSwapProviderNotAllowed`.
6. Sanctions (only when `SanctionsCheckEnabled`) - records the failure in
   `swap/sanctions/audit/<address>`, emits `swap.alert.sanction`, returns
   `ErrSwapSanctioned`.
7. Risk limits, in this order: per-tx min, per-tx max, daily cap, monthly cap,
   velocity.
8. Token `MintPaused` flag, mint-authority configured, recovered signer equals
   the mint authority.
9. Slippage of the voucher amount vs the proof-derived amount.
10. `orderId` not already used; `providerTxId` not already recorded.
11. Sale Pool curve cost vs USD budget (same `SlippageBps` tolerance), Sale
    Pool and admin wallet balances.
12. State updates: record the price proof, transfer ZNHB, mark nonce, write the
    ledger record, record the mint against the risk counters, emit
    `swap.minted` and `swap.mint.proof`.

Price-proof, slippage, duplicate and pool errors are returned as plain errors
and emit no alert event. A block-level failure of the transaction means state
changes are not applied; this document does not verify whether events
appended before a returned error are retained for rejected transactions.

## Events

| Event | Attributes |
| --- | --- |
| `swap.alert.limit_hit` | `provider`, `providerTxId`, `limit` (`provider`, `per_tx_min`, `per_tx_max`, `daily_cap`, `monthly_cap`), `address`, `amount`, and `limitValue` / `currentValue` for the numeric limits |
| `swap.alert.velocity` | `provider`, `providerTxId`, `address`, `windowSeconds`, `allowedMints`, `observedCount` |
| `swap.alert.sanction` | `provider`, `providerTxId`, `address` |

`allowedMints` is never populated by `emitSwapRiskViolation`, so it is always
`0` (`core/swap_voucher_tx.go` lines 622-631).

## Cash-out caps are not enforced

`[swap.risk.cashOut]` (`AssetCaps`, `Tiers`) is parsed into
`RiskParameters.CashOut`, but nothing outside `native/swap/risk.go` reads
`CashOut`. There is no live cash-out-intent path either (see
[stable-ledger.md](stable-ledger.md)).

## Redeem-side limits

`TxTypeRedeemNHB` is checked against `RedeemRiskParameters`
(`native/swap/redeem_risk.go`): per-tx min and max, per-address daily and
monthly caps, counted in separate state keys from mint counters. The values
are the governed `policy.swapRiskParams` values, or the defaults in
[README.md](README.md#redemption-parameters) until a proposal executes. Read
them with the public `swap_getRiskParams` method. A violation fails the
transaction with `redeemNHB: <message>`, for example `amount ... below minimum
...`, `amount ... exceeds maximum ...`, `daily redeem cap ... exceeded`,
`monthly redeem cap ... exceeded` (internal codes `redeem_per_tx_min`,
`redeem_per_tx_max`, `redeem_daily_cap`, `redeem_monthly_cap`).

`policy.swapRiskParams` payload fields: `redeemPerTxMinWei`,
`redeemPerTxMaxWei`, `redeemPerAddressDailyCapWei`,
`redeemPerAddressMonthlyCapWei`, `memo` (`native/governance/types.go`). The
governance validator requires min <= max <= daily <= monthly.

## Pauses

* `Pauses.Swap` (module `swap`) blocks voucher mint, reversal and
  reconciliation transactions with `ErrModulePaused`.
* `Pauses.SwapRedeem` (module `swap_redeem`) blocks only new
  `TxTypeRedeemNHB` burns. Attestations still go through (`config/types.go`).

`examples/docs/ops/read_pauses` (flags `--db`, `--consensus`) prints the
current pause map. `examples/docs/ops/pause_toggle` (flags `--db`,
`--consensus`, `--governance`, `--authority`, `--module`, `--state
pause|resume`) submits a pause change through governance.

## Inspecting local config

```bash
go run ./cmd/swap-audit --config ./config.toml
```

`cmd/swap-audit` prints JSON with `allowedFiat`, the risk caps and velocity
settings, `providers`, and the sanctions deny list. It does not print
`MaxQuoteAgeSeconds`, `SlippageBps` or the cash-out settings.

Use `swap_limits` for live per-address counters ([admin.md](admin.md#swap_limits)).
