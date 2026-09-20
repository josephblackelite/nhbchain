# NHBCoin Tokenomics - Reference Guide

> Applies to: `core/tokenomics/curve`, `core/tokenomics/buyback`, `core/rewards` (halving schedule), the ZNHB pool ledgers in `core/state/manager.go`, and the transaction handlers in `core/state_transition.go`, `core/swap_voucher_tx.go`, `core/buyback_tx.go` and `core/buyback_settlement.go`.

## Table of Contents
1. [Overview](#1-overview)
2. [NHB](#2-nhb)
3. [ZNHB supply and pools](#3-znhb-supply-and-pools)
4. [The Genesis Treasury Distribution Curve](#4-the-genesis-treasury-distribution-curve)
5. [The Reward Pool and the halving schedule](#5-the-reward-pool-and-the-halving-schedule)
6. [The treasury buyback engine](#6-the-treasury-buyback-engine)
7. [What governance can and cannot change](#7-what-governance-can-and-cannot-change)
8. [RPC reference](#8-rpc-reference)
9. [Not in the code](#9-not-in-the-code)

---

## 1) Overview

There are two native tokens. **NHB** has no supply cap constant in the code; `TxTypeMint` (signed vouchers) is its mint transaction and `TxTypeRedeemNHB` its burn transaction. **ZNHB** cannot be minted: `applyMintTransaction` rejects any ZNHB mint with `ErrMintZNHBNotMintable`. The chain's ZNHB supply is whatever genesis allocates, and the admin (treasury) wallet's ZNHB is split into two ledgers, a **Sale Pool** and a **Reward Pool**.

* ZNHB bought from the treasury (`TxTypeBuyZNHB`, or a swap-voucher mint) moves out of the Sale Pool. Nothing is minted for a purchase.
* ZNHB paid as validator/staking/engagement epoch rewards moves out of the Reward Pool.
* If a genesis buyback signer quorum is configured, a share of NHB domain-fee revenue funds a treasury buyback of ZNHB, and bought-back ZNHB is added back to the Sale Pool.
* `CheckZNHBSupplyInvariant` (`core/state_transition.go`) runs every block once the pools are bootstrapped and requires `Sale Pool + Reward Pool == the admin wallet's spendable ZNHB + its locked ZNHB + its pending unbonds + its governance escrow` (`adminZNHBOwned`). A violation is a hard error.

---

## 2) NHB

* **Minting.** `TxTypeMint` carries a signed voucher. The recovered signer must hold the role `MINTER_NHB`; the voucher chain id and expiry are checked; each `invoiceId` can be used once. If `mint.nhb.maxEmissionPerYearWei` is a positive governance value, a mint that would exceed it in the current calendar year fails with `ErrMintEmissionCapExceeded`. Mints increase the tracked total supply.
* **Burning.** `TxTypeRedeemNHB` burns the requested NHB immediately and records a redemption request (`swap.redeem.requested`); per-transaction and per-address caps come from `policy.swapRiskParams` (defaults in [params](../governance/params.md)). Total supply is decreased by the burn.

---

## 3) ZNHB supply and pools

`config/genesis.json` and `config/genesis.mainnet.json` allocate `1000000000000000000000000000` wei (1,000,000,000 ZNHB) to the admin wallet. `EnsureZNHBPoolsBootstrapped` runs once (guarded by a state flag, called from `ProcessBlockLifecycle`) and splits the admin wallet's **live** ZNHB balance at that moment: the Reward Pool gets 20% (floored) and the Sale Pool gets the remainder. It refuses to run when the balance is not positive, and it does not assert any absolute total. For a balance of exactly 1,000,000,000 ZNHB the pools are 800,000,000 (Sale) and 200,000,000 (Reward).

| Pool | Share of the admin wallet's ZNHB at bootstrap | Purpose |
| --- | --- | --- |
| Sale Pool | 80% (the remainder) | Sold through the curve in section 4; also receives bought-back ZNHB (section 6) |
| Reward Pool | 20% | Backs epoch rewards (section 5); also receives forfeited governance deposits and ZNHB transfer fees credited to the admin wallet |

Two effects on the Reward Pool ledger outside epoch rewards, both in `core/state_transition.go`: a ZNHB transfer sent from the admin wallet debits the Reward Pool ledger by the amount sent, and a transfer fee credited to the admin wallet (or a rejected governance proposal's deposit forfeited to it) credits the Reward Pool ledger.

The curve can sell at most 800,000,000 ZNHB in total (16,000 tranches of 50,000, section 4). The Sale Pool ledger is a separate number; a purchase needs both room under the curve and enough Sale Pool and admin-wallet ZNHB.

---

## 4) The Genesis Treasury Distribution Curve

The Sale Pool is sold in **16,000 tranches of 50,000 ZNHB each**. Prices are in NHB per whole ZNHB (USD-equivalent):

```
P(i) = P0 * r^i          i = 0 .. 15,999
P0   = 0.05              (tranche 0)
r    = 20^(1/16000)       (frozen as an exact 50-digit rational, curve.go)
```

`Params.TerminalPrice()` is `P0 * r^16000` = 1.00, the price one step past the last tranche; it is what `znhb_getTokenomicsState` reports once the Sale Pool is fully sold (`fullySoldOut`). The last purchasable tranche (index 15,999) is priced `P0 * r^15999`, just under 1.00. Per-tranche prices are built once by iterative multiplication and rounded to 50 decimal digits at each step (`buildPriceTable`), which keeps every validator's result identical.

**Purchase pricing.** The chain keeps one counter, `cumulative_sale_distributed` (attoZNHB sold so far). A purchase moving it from `c0` to `c1` costs `Params.Cost(c0, c1)`: the exact sum of `price(tranche) * amount-in-that-tranche` across the tranches the range spans, computed with `math/big.Rat`. The exact cost is path-independent (`Cost(a,c) == Cost(a,b) + Cost(b,c)`). The amount actually charged is rounded up to the next attoNHB (`RoundCostUp`), so it can differ by at most one attoNHB per transaction between one large and several small purchases.

Two transaction paths sell from the Sale Pool:

* **Direct purchase**, `TxTypeBuyZNHB` (`0x19`, `applyBuyZNHB`). Payload: `znhbAmount`, `maxNHBAmount`, optional `quoteId`. The chain computes the cost from the live counter and fails with `price moved` if it exceeds `maxNHBAmount`. The buyer pays NHB, which is credited to the admin wallet; the buyer receives ZNHB, the Sale Pool ledger decreases and `cumulative_sale_distributed` increases. The admin wallet cannot buy from itself. Emits `swap.buyznhb.recorded`.
* **Swap-voucher mint**, `applySwapVoucherMintTransaction` (`core/swap_voucher_tx.go`). Besides its price-proof signature and risk checks, it requires the voucher's ZNHB amount to be within `[swap] SlippageBps` (default `50`) of the amount the price proof's rate implies for the voucher's fiat amount (`swap.ComputeMintAmount`), and separately computes the curve cost of that ZNHB amount and requires the voucher's USD budget to be within the same `SlippageBps` of the curve cost. Both checks must pass. It then moves the ZNHB from the admin wallet and Sale Pool to the recipient.

Both fail when `cumulative_sale_distributed` plus the amount would exceed 800,000,000 ZNHB (`curve.ErrExceedsSalePool`); a later buyback can lower the counter.

---

## 5) The Reward Pool and the halving schedule

`core/rewards/halving.go`:

```
B0 = 200 ZNHB per epoch      (HalvingBaseEmissionZNHB)
E  = 500,000 epochs per era  (HalvingEraLengthEpochs)
emission(epoch) = B0 >> floor((epoch-1) / E)     at attoZNHB precision, epoch >= 1
```

The shift rounds down each era; 200 ZNHB is about 2^67.4 attoZNHB, so the per-epoch emission reaches `0` after roughly 68 halvings, and `HalvingEmissionForEpoch` returns `0` for every era from `maxHalvingEras` (80) on. Since `2 * B0 * E` is 200,000,000 ZNHB, the sum of all eras is strictly less than 200,000,000 ZNHB. An "epoch" here is the reward epoch of `epochConfig.Length` blocks.

`StateProcessor.settleEpochRewards` (`core/rewards_logic.go`) reads the Reward Pool ledger: if the epoch's planned payout exceeds the pool balance, the plans are scaled down to the balance, and the pool is debited by what is paid.

`NewNode` (`core/node.go`) activates the schedule with `rewards.HalvingScheduleConfig(2000, 5000, 3000, 2000)` (20% validator / 50% staker / 30% engagement split, history length 2000) whenever an admin wallet is configured. The split and schedule are not reachable by any governance proposal kind.

---

## 6) The treasury buyback engine

The engine is dormant unless the genesis file declares `buybackSigners` and `buybackSignerThreshold` (`GenesisSpec.BuybackSignerConfig`, `core/genesis/spec.go`). Among the genesis files in `config/`, only `genesis.phase-e.json` declares them. When they are absent, `applyBuybackAsk`, `applyBuybackRefPrice` and `settleBuybackEpoch` do nothing or fail with `treasury buyback engine is not configured for this network`.

**Funding.** `applyTransactionFee` (`core/state_transition.go`), the domain-fee path described in [fee policy](../fees/policy.md), credits `feeShareBps` of each NHB fee to the buyback accrual account, a real account balance, and the rest to the fee's route wallet. The default `feeShareBps` is `2000` (20%) and can be changed by a `policy.buybackParams` proposal. The protocol transfer fee (`TransferFeeBps` 20 for NHB, `TransferFeeBpsZNHB` 10 for ZNHB by default) goes entirely to the transfer fee collector and has no buyback share.

**Selling in.** `TxTypeBuybackAsk` (`0x24`) with `znhbAmount`. The seller's ZNHB is moved into the accrual account immediately and the ask is recorded for the current epoch. No price is named. These are rejected: the admin wallet, the accrual account, and any address whose `Stake` is positive.

**Pricing.** Settlement computes

```
MaxBuybackPrice = min( curve_price * (1 - discount_bps/10000),
                       reference_price * (1 - safety_margin_bps/10000) )
```

`curve_price` is the price of the current tranche (the terminal price if sold out). `reference_price` comes from `TxTypeBuybackRefPrice` (`0x25`): a rate (`rateNum/rateDenom`), epoch and timestamp with signatures over the message `NHB_BUYBACK_REFPRICE_V1|epoch=..|rate=..|ts=..` (keccak256, 65-byte signatures), from at least `threshold` distinct genesis signers. Only one reference price is accepted per epoch and the epoch must be the current open epoch. Defaults for `discount_bps` and `safety_margin_bps` are `500` each (`core/node.go`), adjustable by `policy.buybackParams`. If no reference price is on file for the epoch, no purchase happens and every ask is refunded in full.

**Settlement.** `settleBuybackEpoch` runs in `finalizeEpoch` (`core/epochs.go`) right after reward settlement. Every ask is filled pro-rata against the accrual account's NHB balance at `MaxBuybackPrice`: all asks fill fully if total demand fits the budget, otherwise each is scaled by the same ratio (rounding down). Sellers receive NHB for filled ZNHB and get unfilled ZNHB refunded. Filled ZNHB is credited to the admin wallet, the Sale Pool ledger increases by the same amount, and `cumulative_sale_distributed` decreases (floored at `0`). Emits `BuybackEpochSettled`.

---

## 7) What governance can and cannot change

There are 12 proposal kinds ([overview](../governance/overview.md)). Related to this document:

* `policy.buybackParams` sets `feeShareBps`, `discountBps` and `safetyMarginBps`, each `0`-`10000`. The payload has no field for the reference-price signers, which come only from genesis.
* `param.update` can set `mint.nhb.maxEmissionPerYearWei`, `mint.znhb.maxEmissionPerYearWei` (the ZNHB mint path is closed regardless) and the `staking.*` keys, see [params](../governance/params.md).
* `role.allowlist` cannot grant `MINTER_ZNHB`.
* `treasury.directive`, when `TreasuryAllowList` is configured, debits and credits ZNHB account balances directly and does not update the Sale Pool or Reward Pool ledgers.

No proposal kind changes the curve parameters, the pool split, the halving schedule or the reward split.

---

## 8) RPC reference

`znhb_getTokenomicsState` and `znhb_quoteBuy` are read-only and unauthenticated.

### `znhb_getTokenomicsState`

No parameters. Returns zero values (not an error) before the pools are bootstrapped. Example for a freshly bootstrapped 1,000,000,000 ZNHB supply:

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

`buybackAccrualBalanceWei` is the NHB balance recorded for the buyback accrual account.

### `znhb_quoteBuy`

One positional parameter: the ZNHB amount in attoZNHB as a decimal string. Returns the cost `applyBuyZNHB` would charge at the current counter (rounded up):

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

An amount above the remaining curve capacity returns `znhbAmount exceeds the treasury Sale Pool's remaining inventory`. Use `nhbCostWei` plus a buffer as `maxNHBAmount`, since the price moves as other purchases land.

### `buyback_getRefPriceStatus`

Public. Params: `[{"epoch": <uint64>}]` (optional; defaults to the current open epoch). Returns `{epoch, hasRefPrice, rateNum, rateDenom, timestampAt, signerCount}` (the last four are omitted when no price is on file). Returns `buyback epoch scheduling is not enabled on this network` when epochs are not configured.

### `buyback_submitRefPrice`

Requires RPC authentication. Params: `[{"rateNum": "<int>", "rateDenom": "<int>", "epoch": <uint64>, "timestamp": <uint64>, "signatures": ["<hex 65 bytes>", ...]}]`. It wraps the payload in a `TxTypeBuybackRefPrice` transaction and returns `{"txHash": ...}`; signature verification happens on-chain.

---

## 9) Not in the code

* No governance proposal kind or on-chain mechanism gates or schedules the release of future curve tranches. `curve.ReleaseGate` and `AlwaysOpenGate` are defined in `core/tokenomics/curve/gate.go` but are not called by the purchase handlers.
* `znhb_getTokenomicsState` does not return per-epoch buyback asks, settlements or the signer set.
