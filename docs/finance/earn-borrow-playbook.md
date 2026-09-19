# Earn and Borrow: How the Lending Market Works

This note explains the flows behind a "Earn" (supply NHB) and "Borrow" (lock
ZNHB, borrow NHB) interface, as the chain implements them. Field-level detail
is in [lending/on-chain.md](lending/on-chain.md) and
[lending/rpc-api.md](lending/rpc-api.md).

## 1. Earn flow

* A user supplies `NHB` to a pool with a signed `TxTypeLendingSupplyNHB`
  (`0x13`). The NHB moves to the lending module account and the user is
  credited shares.
* Borrowers draw NHB from the same pool. Interest paid by borrowers grows the
  supply index, so a supplier's redeemable NHB (`shares * SupplyIndex`) rises
  over time. The reserve factor share (`config.toml`: 10%) is diverted to
  `FeeAccrual.ProtocolFeesWei` instead of suppliers.
* To exit, the supplier burns shares with `TxTypeLendingWithdrawNHB` (`0x14`),
  which pays out NHB at the current index, limited by available liquidity.
* If nothing has been supplied, there is no NHB to lend: a borrow fails with
  `insufficient liquidity`.

## 2. Borrow flow

* The user deposits `ZNHB` collateral with `TxTypeLendingDepositZNHB` (`0x15`).
* The chain values collateral in NHB using the on-chain reference price from
  `TxTypeLendingRefPrice`. With no reference price yet, collateral is valued
  1:1 with NHB.
* A borrow (`TxTypeLendingBorrowNHB`, `0x17`) must satisfy
  `collateralValue * MaxLTV >= debt * 10000` and
  `collateralValue * LiquidationThreshold >= debt * 10000`, and needs enough
  available pool liquidity.

Parameters in the repository `config.toml` (`[lending]`):

* `MaxLTVBps = 6000` (60%)
* `LiquidationThresholdBps = 8500` (85%)

Example with a reference price of 0.05 NHB per ZNHB (NHB is treated as 1 USD in
the RPC's USD figures):

* Deposit `50,000 ZNHB` -> collateral value `2,500 NHB`.
* At 60% MaxLTV the maximum total debt is `1,500 NHB`.
* A `250 NHB` borrow passes if the pool has at least `250 NHB` available.

## 3. Where borrowed NHB comes from

Borrowed NHB is the pool's supplied NHB. Repayments return NHB to the module
account and interest raises the supply index.

## 4. State that a UI should read from the chain

Pool liquidity, collateral, and debt are all on-chain and readable with
`lending_getMarket`, `lending_getUserAccount` and the fixed-term getters
([lending/rpc-api.md](lending/rpc-api.md)). Nothing on the chain records an
activity history for lending actions: supply, borrow, repay and liquidate emit
no lending events (`core/lending_native.go`), so a history view must be
rebuilt from the transactions themselves.

## 5. Summary

* **Earn**: supply NHB, receive shares that appreciate with borrower interest.
* **Borrow**: lock ZNHB, borrow NHB against it.
* **Reference price**: values ZNHB collateral in NHB terms.
* **Risk checks**: `MaxLTV` on borrow, `LiquidationThreshold` for health and
  liquidation.
* **Pool liquidity**: bounds every borrow and withdrawal.

## 6. If a user with collateral cannot borrow

Check in this order, using the errors the engine returns:

1. Is the collateral recorded? `lending_getUserAccount` -> `collateralZnhbWei`.
2. Is there liquidity? `lending_getMarket` -> `availableLiquidityWei`
   (`insufficient liquidity` otherwise).
3. Is the price guard failing? `lending_getRefPriceStatus`: with the shipped
   `OracleMaxAgeBlocks = 1000`, a borrow returns `oracle quote stale` if no
   reference price was ever accepted or the last one is older than 1000
   blocks; `oracle deviation too large` if the last update moved the price by
   more than 50% (`OracleMaxDeviationBps = 5000`).
4. Would the borrow exceed the limits? `borrow would exceed maximum
   loan-to-value ratio` or `borrower health factor below 1`.
5. Is the `lending` module paused? The engine returns the module-pause error
   (`ErrModulePaused`) for every action.
