# NHBCoin Tokenomics – Reference Guide

> Status: **Live** (Sale Pool curve pricing) / **Built, not yet activated** (validator/staking reward halving schedule)
> Applies to: `core/tokenomics/curve`, `core/state/manager.go` (ZNHB pool ledgers), `core/rewards` (halving schedule)

## Table of Contents
1. [Overview](#1-overview)
2. [NHB — the commerce currency](#2-nhb--the-commerce-currency)
3. [ZNHB — the fixed-supply network asset](#3-znhb--the-fixed-supply-network-asset)
4. [The Genesis Treasury Distribution Curve](#4-the-genesis-treasury-distribution-curve)
5. [The Reward Pool and the validator/staking halving schedule](#5-the-reward-pool-and-the-validatorstaking-halving-schedule)
6. [What governance can and cannot change](#6-what-governance-can-and-cannot-change)
7. [RPC reference](#7-rpc-reference)
8. [What this document does not claim](#8-what-this-document-does-not-claim)

---

## 1) Overview

NHBCoin runs a deliberate two-token model. **NHB** is the commerce currency: an elastic, deposit-backed unit meant to feel like spending dollars. **ZNHB** is the network's fixed-supply security and scarcity asset: exactly 1,000,000,000 ZNHB exist, forever, split once at genesis into two purpose-built pools that never mix.

**Funding invariants**

* Every ZNHB a buyer receives from the treasury — through a direct purchase or a swap-voucher mint — moves out of the **Sale Pool**, priced by the Genesis Treasury Distribution Curve. Nothing is minted to satisfy a purchase.
* Every ZNHB a validator or staker is meant to receive as a network reward is designed to move out of the separate **Reward Pool**, following a halving schedule. This mechanism exists and is enforced in code, but is **not yet turned on** in production — see [§5](#5-the-reward-pool-and-the-validatorstaking-halving-schedule).
* `core/state_transition.go`'s `CheckZNHBSupplyInvariant` asserts, every block, that `Sale Pool balance + Reward Pool balance == the treasury wallet's live ZNHB balance`. A violation is a hard consensus error, not a warning.

---

## 2) NHB — the commerce currency

NHB is the settlement and payments rail: mint-on-deposit, burn-on-redemption, no fixed supply cap. It is designed to be backed 1:1 by custodied reserves (USDT/USDC via the swap/OTC pipeline) — this document makes no claim about NHB's price beyond that backing relationship; it is not a speculative asset.

---

## 3) ZNHB — the fixed-supply network asset

ZNHB has a hard genesis supply of **1,000,000,000 ZNHB**, split once, permanently, the first time a real admin/treasury wallet is configured (`StateProcessor.EnsureZNHBPoolsBootstrapped`, `core/state_transition.go`):

| Pool | Size | Purpose |
| --- | --- | --- |
| Sale Pool | 800,000,000 ZNHB (80%) | Sold to buyers via the Genesis Treasury Distribution Curve |
| Reward Pool | 200,000,000 ZNHB (20%) | Backs validator/staking rewards via a halving schedule (not yet activated) |

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

Validator and staking rewards are designed to be funded from the 200,000,000-ZNHB Reward Pool, using a Bitcoin-style halving schedule (`core/rewards/halving.go`):

```
B0 = 50,000 ZNHB / epoch     (base emission, era 0)
E  = 2,000 epochs / era      (era length)
emission(epoch) = B0 >> floor((epoch-1) / E)
```

Integer halving is applied at attoZNHB precision (a right shift, rounding down every era), which means the cumulative emission across every era converges to **strictly less than 200,000,000 ZNHB**, forever — it approaches the Reward Pool's exact size without ever reaching or exceeding it, the same convergence property that gives Bitcoin's own 21,000,000-BTC cap its guarantee.

`StateProcessor.settleEpochRewards` (`core/rewards_logic.go`) enforces this at the ledger level, independent of the emission formula: whatever an epoch's schedule calls for is clamped to the Reward Pool's live remaining balance before any validator or staker is paid, and the pool is debited by the exact amount actually paid out — never the nominal, requested amount. If the pool is ever fully drawn down, further epochs pay zero rather than manufacturing new ZNHB.

**Current status: built and tested, not yet activated.** The reward emission schedule (`rewards.Config.Schedule`) is empty by default and stays that way until something explicitly activates it — there is currently no config file, genesis field, RPC method, or governance proposal kind that populates it. No validator or staking reward is being paid out on the network today. `rewards.HalvingScheduleConfig(...)` is the constructor a future activation path (genesis parameter, governance kind, or operator tooling) would call.

---

## 6) What governance can and cannot change

NHBChain's live governance system (7 proposal kinds — parameter updates, slashing policy, fee rate changes, swap price-signer registration, and two disabled-by-default kinds for role allowlists and treasury directives) is documented in full, with exact mechanics (POTSO-weighted voting, quorum, timelock), in the nhbportal wallet's **Governance → How governance works** tab.

The short version for this document: **none of it reaches ZNHB's tokenomics.** The Sale Pool split, the curve's price/tranche parameters, and the Reward Pool's halving schedule are genesis-fixed constants and one-time bootstrap logic — not parameters in the governable `ParamStore`, and not reachable by any proposal kind that exists today. This is deliberate: a fixed-supply asset's guarantees only hold if they can't be voted away by whoever shows up to a proposal.

---

## 7) RPC reference

Both methods are public (no authentication required) and read-only.

### `znhb_getTokenomicsState`

Returns the curve's live position and both pool balances. No parameters.

```json
{
  "currentTranchePrice": "0.050000000000000000",
  "currentTrancheIndex": 0,
  "fullySoldOut": false,
  "cumulativeSaleDistributedWei": "0",
  "salePoolBalanceWei": "800000000000000000000000000",
  "rewardPoolBalanceWei": "200000000000000000000000000",
  "buybackAccrualBalanceWei": "0"
}
```

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

## 8) What this document does not claim

* ZNHB has no protocol-defined valuation ceiling, floor, or price guarantee once it leaves the treasury.
* No treasury buyback mechanism exists on-chain today. If one ships, its signer quorum is designed to stay outside governance's reach permanently, by intent.
* Validator/staking rewards are not currently being paid on this network — see §5.
* NHB's backing claim (1:1 custodied reserves) describes the intended architecture; verifying live reserve custody is outside the scope of this document.
