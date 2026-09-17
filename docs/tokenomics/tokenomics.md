# NHBCoin Tokenomics – Reference Guide

> Status: **Live** (Sale Pool curve pricing; validator/staking reward halving schedule; treasury buyback engine)
> Applies to: `core/tokenomics/curve`, `core/tokenomics/buyback`, `core/state/manager.go` (ZNHB pool ledgers), `core/rewards` (halving schedule)

## Table of Contents
1. [Overview](#1-overview)
2. [NHB — the commerce currency](#2-nhb--the-commerce-currency)
3. [ZNHB — the fixed-supply network asset](#3-znhb--the-fixed-supply-network-asset)
4. [The Genesis Treasury Distribution Curve](#4-the-genesis-treasury-distribution-curve)
5. [The Reward Pool and the validator/staking halving schedule](#5-the-reward-pool-and-the-validatorstaking-halving-schedule)
6. [The treasury buyback engine](#6-the-treasury-buyback-engine)
7. [What governance can and cannot change](#7-what-governance-can-and-cannot-change)
8. [RPC reference](#8-rpc-reference)
9. [What this document does not claim](#9-what-this-document-does-not-claim)

---

## 1) Overview

NHBCoin runs a deliberate two-token model. **NHB** is the commerce currency: an elastic, deposit-backed unit meant to feel like spending dollars. **ZNHB** is the network's fixed-supply security and scarcity asset: exactly 1,000,008,000 ZNHB exist, forever, split once at genesis into two purpose-built pools that never mix (the 8,000 ZNHB above the originally-intended 1,000,000,000 figure is a documented reconciliation remainder from pre-existing mint-path bugs already fixed in the code that produced the live genesis snapshot — see §3).

**Funding invariants**

* Every ZNHB a buyer receives from the treasury — through a direct purchase or a swap-voucher mint — moves out of the **Sale Pool**, priced by the Genesis Treasury Distribution Curve. Nothing is minted to satisfy a purchase.
* Every ZNHB a validator or staker receives as a network reward moves out of the separate **Reward Pool**, following a halving schedule — see [§5](#5-the-reward-pool-and-the-validatorstaking-halving-schedule).
* A share of NHB transaction-fee revenue automatically funds a **treasury buyback** that repurchases ZNHB from willing sellers and recycles it back into the Sale Pool, never burning it and never minting new supply — see [§6](#6-the-treasury-buyback-engine).
* `core/state_transition.go`'s `CheckZNHBSupplyInvariant` asserts, every block, that `Sale Pool balance + Reward Pool balance == the treasury wallet's live ZNHB balance`. A violation is a hard consensus error, not a warning.

---

## 2) NHB — the commerce currency

NHB is the settlement and payments rail: mint-on-deposit, burn-on-redemption, no fixed supply cap. It is designed to be backed 1:1 by custodied reserves (USDT/USDC via the swap/OTC pipeline) — this document makes no claim about NHB's price beyond that backing relationship; it is not a speculative asset.

---

## 3) ZNHB — the fixed-supply network asset

ZNHB has a hard genesis supply of **1,000,008,000 ZNHB**, split once, permanently, the first time a real admin/treasury wallet is configured (`StateProcessor.EnsureZNHBPoolsBootstrapped`, `core/state_transition.go`):

| Pool | Size | Purpose |
| --- | --- | --- |
| Sale Pool | 800,008,000 ZNHB | Sold to buyers via the Genesis Treasury Distribution Curve; also receives bought-back ZNHB from the treasury buyback engine ([§6](#6-the-treasury-buyback-engine)) |
| Reward Pool | 200,000,000 ZNHB | Backs validator/staking rewards via a halving schedule |

The Sale Pool's ledger balance carries an extra 8,000 ZNHB beyond the curve's own sellable cap (16,000 tranches × 50,000 ZNHB = 800,000,000 ZNHB exactly). That 8,000 ZNHB is a permanent, documented reconciliation remainder from pre-existing mint-path bugs (since fixed) baked into the live genesis snapshot before this reconciliation — it sits in the Sale Pool ledger but is never reachable by an ordinary curve purchase, and the Reward Pool stays at exactly 200,000,000 ZNHB so the halving schedule's convergence proof (§5) remains exact.

ZNHB makes **no protocol-defined valuation promise**. The curve in §4 governs only how treasury-owned Sale Pool inventory is priced as it sells down — it is not a ceiling, floor, or guarantee on what ZNHB trades for once it leaves the treasury and moves peer-to-peer.

---

## 4) The Genesis Treasury Distribution Curve

The Sale Pool is not sold at a flat price. It is divided into **16,000 tranches of 50,000 ZNHB each**, and each tranche is priced higher than the last:

```
P(i) = P0 · r^i
P0 = $0.05           (tranche 0 spot price)
r  = 20^(1/16000)     (frozen at genesis, identical across every validator)
```

The terminal price — the spot price of the last tranche, approached but never exceeded within the Sale Pool's own inventory — is **$1.00**. That $1.00 describes only what the treasury itself would charge for its very last unit of Sale Pool inventory; it is not a market cap, a price ceiling, or a promise about ZNHB's value once it trades peer-to-peer.

**How a purchase is actually priced:** the chain tracks one consensus counter, `cumulative_sale_distributed` — the running total of ZNHB the Sale Pool has ever sold. A purchase's cost is the exact integral of `P(i)` across the tranche boundaries it spans (`core/tokenomics/curve.Params.Cost`), computed in exact rational arithmetic (`math/big.Rat`, never floats) so every validator derives an identical result. This makes the pricing **immune to order-splitting**: buying 100,000 ZNHB in one transaction costs exactly the same as buying it in ten 10,000-ZNHB transactions.

**Two on-chain paths draw from the Sale Pool, both curve-priced, both real:**

* **Direct purchase** (`TxTypeBuyZNHB`, `core/state_transition.go`'s `applyBuyZNHB`) — the buyer specifies the ZNHB amount they want and a maximum NHB they're willing to pay (slippage protection); the chain computes the exact cost from the live curve position and rejects the transaction if it exceeds the buyer's cap.
* **Swap-voucher mint** (`applySwapVoucherMintTransaction`, `core/swap_voucher_tx.go`) — an OTC/fiat on-ramp path that independently verifies the requested ZNHB amount against the curve's own price before moving it out of the Sale Pool, in addition to its existing price-proof and fraud-control checks.

Neither path can create ZNHB beyond what the Sale Pool has left — both fail cleanly (a retriable error, not a permanent one, since a future buyback could free up room) once `cumulative_sale_distributed` would exceed 800,000,000 ZNHB.

---

## 5) The Reward Pool and the validator/staking halving schedule

Validator and staking rewards are funded from the 200,000,000-ZNHB Reward Pool, using a Bitcoin-style halving schedule (`core/rewards/halving.go`):

```
B0 = 200 ZNHB / epoch        (base emission, era 0)
E  = 500,000 epochs / era    (era length)
emission(epoch) = B0 >> floor((epoch-1) / E)
```

Integer halving is applied at attoZNHB precision (a right shift, rounding down every era), which means the cumulative emission across every era converges to **strictly less than 200,000,000 ZNHB**, forever — it approaches the Reward Pool's exact size without ever reaching or exceeding it, the same convergence property that gives Bitcoin's own 21,000,000-BTC cap its guarantee.

`StateProcessor.settleEpochRewards` (`core/rewards_logic.go`) enforces this at the ledger level, independent of the emission formula: whatever an epoch's schedule calls for is clamped to the Reward Pool's live remaining balance before any validator or staker is paid, and the pool is debited by the exact amount actually paid out — never the nominal, requested amount. If the pool is ever fully drawn down, further epochs pay zero rather than manufacturing new ZNHB.

**Current status: live.** `core/node.go`'s `NewNode` activates the schedule automatically (`rewards.HalvingScheduleConfig(2000, 5000, 3000, 2000)` — 20% validator / 50% staker / 30% engagement split) whenever a real admin/treasury wallet is configured for the network. There is no separate opt-out: any network that has a working ZNHB Sale Pool (§3, §4) also has this schedule active. `rewards.Config.Schedule`/the validator/staker/engagement split percentages are not currently reachable by any governance proposal kind — changing them requires a code change and a redeploy, not a vote.

---

## 6) The treasury buyback engine

A share of every NHB-denominated transaction fee automatically funds a treasury buyback: a per-epoch, budget-capped repurchase of ZNHB from willing sellers, recycled back into the Sale Pool (never burned, never re-minted). This closes the loop the other direction from §4 — instead of only ever selling Sale Pool inventory outward, the treasury can also buy ZNHB back in when it makes sense to.

**Funding.** `core/state_transition.go`'s `applyTransactionFee` sweeps a configurable share (`fee_share_bps`, launch default **20%**) of NHB fee revenue into a dedicated on-chain Buyback Accrual account (`core/tokenomics/buyback`) — a real account balance, not a virtual counter, so there is always genuine NHB behind whatever the engine later pays sellers. The remaining share still routes to the fee's normal owner wallet exactly as before; this is a no-op change in NHB actually collected from payers. That underlying revenue is the separate, opt-in merchant/POS domain fee (keyed on `tx.MerchantAddress`, `native/fees`, default **150 bps (1.50%)** MDR) — distinct from the protocol-enforced network transfer fee (`core/transfer_gas_policy.go`'s `TransferGasPolicy`), which applies to ordinary transfers once a wallet's free tier is exhausted, at its own per-asset rate — **20 bps (0.20%) on NHB transfers**, **10 bps (0.10%) on ZNHB transfers** — and is routed entirely to `TransferGasPolicy.FeeCollector`, with no buyback share of its own. NHB's transfer rate is kept higher deliberately, to generate revenue and encourage holding NHB, while ZNHB's lower rate reflects its own use case as a lower-priced asset.

**Selling in.** Any ZNHB holder can submit a market ask (`TxTypeBuybackAsk`) at any point during an epoch, naming the amount of ZNHB they're willing to sell — no price is named; the treasury sets the price (see below). The ask's ZNHB is escrowed into the Buyback Accrual account immediately on submission, not just promised, so a seller can never ask for more than they actually have. The treasury's own admin/treasury wallet and any address currently holding bonded validator stake are protocol-barred from selling in — an obvious conflict-of-interest a treasury shouldn't be able to trade against itself, or that a validator shouldn't be able to exploit around its own participation in finalizing the very epoch that settles it.

**Pricing.** Settlement never trusts a single number. It computes a hard ceiling, `MaxBuybackPrice`, as the *lesser* of two independently derived prices:

```
MaxBuybackPrice = min(
  curve_price      × (1 − discount_bps),        // the Sale Pool's own live spot price, discounted
  reference_price   × (1 − safety_margin_bps),   // an independently signed external price, margined
)
```

`curve_price` is the Genesis Treasury Distribution Curve's live spot price (§4) — no separate oracle needed for this half. `reference_price` comes from a genesis-declared, permanently non-governable M-of-N signer quorum (`TxTypeBuybackRefPrice`): a bundle of signatures over a canonical per-epoch price message, verified the same way `native/escrow`'s frozen-arbitration signer sets are (independently reimplemented, not imported, to keep the two domains from coupling). If no valid reference price is signed and submitted for an epoch, the treasury does not guess — no purchase happens that epoch, and every pending ask is refunded in full.

**Settlement.** Once per epoch, at the same finalization point where validator/staking rewards settle (`core/buyback_settlement.go`'s `settleBuybackEpoch`, called from `core/epochs.go`'s `finalizeEpoch`), every pending ask is filled pro-rata against the Buyback Accrual account's live NHB balance at `MaxBuybackPrice`: if total demand fits inside budget, every seller is filled in full; if demand exceeds budget, every seller is scaled down by the identical ratio, so no seller is filled while another is starved. Filled ZNHB moves into the treasury's own admin wallet and the Sale Pool balance grows by the same amount — recycled inventory, not new supply — while `cumulative_sale_distributed` (§4) decrements accordingly, making the curve's very next tranche a little cheaper than it would otherwise have been. Sellers are paid NHB for whatever filled, and refunded ZNHB for whatever didn't.

**Current status: live mechanism, governance-adjustable parameters.** The engine activates automatically whenever a genesis-declared buyback signer quorum is configured (`genesis.BuybackSignerConfig`) — a network without one behaves exactly as if this section didn't exist, zero behavioral change. `fee_share_bps` (20% at launch), `discount_bps` (5%), and `safety_margin_bps` (5%) start as code-level defaults but are adjustable via the `policy.buybackParams` governance proposal kind (`native/governance`), each within `[0, 10000]` bps, applied via the normal quorum/threshold/timelock gate every other proposal kind uses. The signer quorum itself has no field in that payload and no governance path at all — see [§7](#7-what-governance-can-and-cannot-change).

---

## 7) What governance can and cannot change

NHBChain's live governance system (8 proposal kinds — parameter updates, slashing policy, fee rate changes, swap price-signer registration, treasury buyback parameters, and two disabled-by-default kinds for role allowlists and treasury directives) is documented in full, with exact mechanics (POTSO-weighted voting, quorum, timelock), in the nhbportal wallet's **Governance → How governance works** tab.

The short version for this document: **almost none of it reaches ZNHB's tokenomics.** The Sale Pool split, the curve's price/tranche parameters, and the Reward Pool's halving schedule are genesis-fixed constants and one-time bootstrap logic — not parameters in the governable `ParamStore`, and not reachable by any proposal kind that exists today. The one deliberate exception is the buyback engine's three bps parameters: `fee_share_bps`/`discount_bps`/`safety_margin_bps` are governance-adjustable via `policy.buybackParams` (`native/governance`'s `ProposalKindBuybackParams`), each bounded to `[0, 10000]` and gated by the same quorum/threshold/timelock every other proposal goes through. Its M-of-N reference-price signer quorum has **no** governance path at all, permanently, by the same architecture that keeps everything else in this document out of governance's reach — `BuybackParamsPayload` has no field for it, and no other proposal kind can touch it either. A fixed-supply asset's guarantees only hold if the things that matter can't be voted away by whoever shows up to a proposal.

---

## 8) RPC reference

Both methods are public (no authentication required) and read-only.

### `znhb_getTokenomicsState`

Returns the curve's live position, both pool balances, and the treasury buyback engine's current NHB accrual balance. No parameters.

```json
{
  "currentTranchePrice": "0.050000000000000000",
  "currentTrancheIndex": 0,
  "fullySoldOut": false,
  "cumulativeSaleDistributedWei": "0",
  "salePoolBalanceWei": "800008000000000000000000000",
  "rewardPoolBalanceWei": "200000000000000000000000000",
  "buybackAccrualBalanceWei": "0"
}
```

`buybackAccrualBalanceWei` mirrors the Buyback Accrual account's live NHB balance (§6) — NHB fee revenue pending the next epoch's settlement. This RPC does not yet expose per-epoch ask/settlement history or the reference-price signer set; that is tracked follow-up work, not yet built.

### `znhb_quoteBuy`

Returns the exact NHB cost of buying a given amount of ZNHB from the Sale Pool right now — the same figure `applyBuyZNHB` would charge if the transaction were submitted immediately. Takes one positional parameter: the ZNHB amount in attoZNHB (wei), as a decimal string.

```
params: ["1000000000000000000"]   // 1 ZNHB
```

```json
{
  "znhbAmountWei": "1000000000000000000",
  "nhbCostWei": "50000000000000000",
  "effectiveRate": "0.050000000000000000"
}
```

Callers building a `TxTypeBuyZNHB` transaction should use `nhbCostWei` plus a small slippage buffer as the transaction's `maxNHBAmount` — never approximate the curve client-side, since it is a bonding curve and its price moves as other purchases land between a quote and a transaction's execution.

---

## 9) What this document does not claim

* ZNHB has no protocol-defined valuation ceiling, floor, or price guarantee once it leaves the treasury.
* The treasury buyback engine (§6) is real, tested code, but only activates on a network whose genesis actually configures a reference-price signer quorum — this document does not claim any *specific* deployed network has done so, only that the mechanism exists and behaves as described once it has.
* No specific deployed network has necessarily passed a `policy.buybackParams` proposal yet; this document does not claim any network's current bps values have ever diverged from the code-level defaults, only that the proposal kind exists and works — see §6/§7.
* `policy.trancheGating` (future-tranche release conditions on the Genesis Treasury Distribution Curve, §4) is a separate, still-undesigned governance kind — not part of the buyback engine, and not built.
* `znhb_getTokenomicsState` does not yet expose per-epoch buyback ask/settlement history or the reference-price signer set as structured data — only the current accrual balance.
* NHB's backing claim (1:1 custodied reserves) describes the intended architecture; verifying live reserve custody is outside the scope of this document.
