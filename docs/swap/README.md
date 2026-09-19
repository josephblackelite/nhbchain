# Swap Module

The swap module covers four on-chain flows plus a set of read/admin RPC
methods. Everything below is taken from the node source; where a piece of
code exists but no live path reaches it, that is stated.

## On-chain flows

| Flow | Transaction type | What the chain does |
| --- | --- | --- |
| Voucher mint (fiat on-ramp) | `TxTypeSwapVoucherMint` (`0x1E`) | Verifies a mint-authority-signed voucher and a signed price proof, then **transfers ZNHB** from the admin wallet / treasury Sale Pool to the recipient. It does not mint new ZNHB. See [overview.md](overview.md). |
| NHB mint | `mint_with_sig` RPC (see [`docs/escrow/mint-settlement.md`](../escrow/mint-settlement.md)) | Mints `NHB` only; `ZNHB` is rejected with `ErrMintZNHBNotMintable` (`core/node.go` `MintWithSignature`). |
| ZNHB purchase | `TxTypeBuyZNHB` (`0x19`) | Buyer pays NHB to the admin wallet; the chain computes the price from the Genesis Treasury Distribution Curve and moves ZNHB from the Sale Pool to the buyer (`core/state_transition.go` `applyBuyZNHB`). |
| NHB redemption (swap-out) | `TxTypeRedeemNHB` (`0x1B`), `TxTypeAttestRedemption` (`0x1C`) | Redeem burns the sender's NHB and stores a `pending` request; a holder of `ROLE_SWAP_PAYOUT_ATTESTOR` later marks it `paid` or `failed`. `failed` re-credits the burned NHB. See [risk-controls.md](risk-controls.md). |

`TxTypeSwapMint` (`0x11`) and `TxTypeSwapBurn` (`0x12`) are disabled: both
return "native on-chain swap mint/burn is disabled -- use the buyZNHB
transaction type instead" (`core/state_transition.go` `applySwapMint`,
`applySwapBurn`).

The off-chain systems that produce vouchers, price proofs, and redemption
attestations are outside the scope of this repository. This node only
verifies what an off-chain submitter hands it.

## Units and the NHB peg

* Amounts are wei (18 decimals) as decimal strings.
* At startup `cmd/nhb/main.go` seeds the in-process manual oracle with
  `USD -> NHB = 1.0` and `USD -> ZNHB` from the `NHB_ZNHB_ORACLE_PRICE_USD`
  environment variable, defaulting to `0.05` (`resolveZNHBOraclePrice`).
  Nothing on the current consensus path reads these seeds; see
  [oracle.md](oracle.md).

## RPC methods

Method names come from the dispatch switch in `rpc/http.go`.

| Method | Access |
| --- | --- |
| `swap_submitVoucher`, `swap_voucher_get`, `swap_voucher_list`, `swap_voucher_export` | Listed in `isPublicSwapMethod`. When swap partner authentication is configured (`s.swapAuth != nil`), requests must pass it (`rpc/http.go` around line 1359); no JWT is required. |
| `swap_getRiskParams`, `swap_getRedemptionFeeParams` | Public (same list). |
| `swap_limits`, `swap_provider_status`, `swap_burn_list`, `swap_voucher_reverse`, `swap_markReconciled`, `swap_setManualQuote`, `swap_listPendingRedemptions` | Require the RPC bearer JWT (`requireAuthInto`). |
| `nhb_requestSwapApproval`, `nhb_getSwapQuote`, `nhb_swapMint`, `nhb_swapBurn`, `nhb_getSwapStatus`, `nhb_checkSwapAllowance` | Listed in `isPublicSwapMethod` (partner authentication when configured) **and** require the RPC JWT (`requireAuthInto`). Quote/reservation methods served by a stable-quote engine attached to the node; they return `stable engine not enabled` when none is attached. `nhb_checkSwapAllowance` always returns `allowed: true` (`rpc/swap_stable_handlers.go`). |
| `nhb_getOraclePrice` | Public. Served from the same attached engine. |

* `swap_getRiskParams` returns `{"redeem": {perTxMinWei, perTxMaxWei, perAddressDailyCapWei, perAddressMonthlyCapWei}}`.
* `swap_getRedemptionFeeParams` returns `{feeBps, feeFloorWei, feeCapWei}`.
* `swap_listPendingRedemptions` returns `{"requests": [...]}` with `requestId`, `nhbAmountWei`, `destinationAsset`, `destinationAddress`, `status`, `createdAt`, and optionally `account`, `settledAt`, `payoutReference`, `failureReason`.

## Redemption parameters

Redeem limits are governed on-chain (`policy.swapRiskParams`) and fall back to
built-in defaults until a proposal executes (`native/swap/redeem_risk.go`):

| Parameter | Default |
| --- | --- |
| per-transaction minimum | 5 NHB (`5000000000000000000` wei) |
| per-transaction maximum | 1,000 NHB |
| per-address daily cap | 2,000 NHB |
| per-address monthly cap | 20,000 NHB |

The redemption fee (`policy.redemptionFeeParams`) defaults to 100 bps, a floor
of 1 NHB and a cap of 1,000 NHB (`native/swap/redemption_fee.go`). The fee is
computed by `ComputeRedemptionFee`; the burn transaction itself does not
deduct it on-chain.

## Related documents

* [overview.md](overview.md) - voucher wire format and `swap_submitVoucher`
* [oracle-verification.md](oracle-verification.md) - price proofs
* [oracle.md](oracle.md) - oracle aggregator and provider status
* [risk-controls.md](risk-controls.md) - configuration and limits
* [admin.md](admin.md) - reversal, reconciliation, admin RPC methods
* [treasury.md](treasury.md) - burn receipts
* [stable-ledger.md](stable-ledger.md) - stable-asset ledger records
* [state-indexes.md](state-indexes.md) - state keys and query paths
