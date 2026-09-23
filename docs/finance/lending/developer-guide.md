# Lending Developer Guide

Recommended flow for building on the native lending module with JSON-RPC.
Reads use RPC methods; every state change is a signed transaction submitted
with `nhb_sendTransaction` ([rpc-api.md](rpc-api.md)).

## 1. Discover pools and risk configuration

* Call [`lend_getPools`](rpc-api.md#lend_getpools) to list pools, or
  [`lending_getMarket`](rpc-api.md#lending_getmarket) with a `poolId` for one
  pool. Both return `riskParameters`; the fields the node actually sets are
  `MaxLTV`, `LiquidationThreshold`, `DeveloperFeeCapBps` and `Oracle`
  ([on-chain.md](on-chain.md#risk-parameters)).
* `depositApyBps`, `borrowApyBps` and `availableLiquidityWei` on each market are
  computed per call; use `availableLiquidityWei` rather than `TotalNHBSupplied`
  to see what can be borrowed or withdrawn.
* Read `lending_getRefPriceStatus` to see whether a reference price exists.
  Without one, collateral is valued 1:1 with NHB, and with the shipped
  `OracleMaxAgeBlocks = 1000` borrows are refused until one is accepted.
* Create additional pools with a signed `TxTypeLendingCreatePool` (`0x2C`); the
  transaction signer becomes the pool's developer owner. The `default` pool
  cannot be created explicitly.

## 2. Authenticate

* Reads need no credential. `lending_getMarket`, `lend_getPools` and
  `lending_getUserAccount` share a small query pool: on HTTP 429 (code
  `-32020`) wait for the `Retry-After` header and try again.
* `nhb_sendTransaction` requires the RPC credential: a JWT sent as
  `Authorization: Bearer <jwt>`, or a verified client certificate when the
  server requires one.
* Each transaction is authorised by the sender's own signature and nonce. There
  is no address field to spoof.

## 3. Load the account snapshot

* [`lending_getUserAccount`](rpc-api.md#lending_getuseraccount) for the pool. A
  `404` `account not found` means the address has no record in that pool: show
  zero balances.
* `collateralZnhbWei` is the withdrawable collateral figure. `collateralValueUsd`
  is `""` when no reference price exists.
* `lending_getFixedTermLoan` and `lending_getFixedTermDeposit` return the
  fixed-term positions; `borrowedValueUsd` on the account already includes an
  active fixed-term loan.

## 4. Supply and manage collateral

1. Supply NHB with `TxTypeLendingSupplyNHB` (`value` = NHB in wei). A pool with
   no shares needs at least 1 NHB for the first supply.
2. Deposit ZNHB collateral with `TxTypeLendingDepositZNHB`.
3. Withdraw collateral with `TxTypeLendingWithdrawZNHB` (refused if the position
   would become unhealthy). Withdraw supply with `TxTypeLendingWithdrawNHB`,
   where `value` is the amount of **shares** to burn (compute it from
   `supplied[].amountWei` and the market's `SupplyIndex`); a withdrawal in the
   same block as the account's supply is refused.

## 5. Borrow

* Pre-check the borrow client-side with the rules in
  [on-chain.md](on-chain.md#health-and-borrow-limits): after the borrow,
  `collateralValue * MaxLTV >= debt * 10000`.
* Send `TxTypeLendingBorrowNHB` (`value` = NHB to borrow). If the pool has a
  developer fee configured, it is applied to every borrow (the borrower still
  receives `value`; the fee is added to the debt). Set `useDeveloperFee: true`
  only if you want the transaction to fail when no fee is configured.
* For a locked-rate loan use `TxTypeLendingBorrowFixedTerm` with
  `tenureDays` from `lending_getRateSchedule`.

## 6. Repay

* `TxTypeLendingRepayNHB`: any `value` is accepted and the engine repays at most
  the current debt. Fixed-term loans are repaid with
  `TxTypeLendingRepayFixedTerm`.
* Refresh the account snapshot after the transaction is included in a block.

## 7. Liquidation monitoring

* Poll `lending_getUserAccount` and compare `collateralValueUsd` and
  `borrowedValueUsd` against the liquidation threshold from `riskParameters`.
* A liquidator signs `TxTypeLendingLiquidate` with the borrower's address in the
  payload. It repays the borrower's entire flexible debt, so the liquidator's
  NHB balance must cover it.

## Operational checks

* The `lending` module pause appears in the on-chain pause map. Inspect it with
  `go run ./examples/docs/ops/read_pauses` (flags `--db`, `--consensus`); the
  pause-toggle example (`examples/docs/ops/pause_toggle`) submits a governance
  change.
* Engine errors carry the `lending engine:` prefix and are listed in
  [rpc-api.md](rpc-api.md#errors).

## Best practices

* A transaction hash returned by `nhb_sendTransaction` is a mempool
  acknowledgement only; confirm the effect by reading state after inclusion.
* Validate amounts as positive integers before signing.
* `lending_getMarket` returns `SupplyIndex` and `BorrowIndex` (1e27 scale) if
  you show yield or debt growth.
* Reference-price and risk values come from node config and signed submissions,
  so re-read them rather than caching them across restarts.
