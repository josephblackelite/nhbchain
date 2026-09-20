# NHBChain Lending Overview

The native lending module is an NHB money market with ZNHB collateral. This
page summarises what the code implements; [on-chain.md](on-chain.md) has the
accounting details and [rpc-api.md](rpc-api.md) the wire interfaces.

## Core concepts

### Supplying NHB
A supplier sends NHB into a pool (`TxTypeLendingSupplyNHB`) and is credited
**shares** (`SupplyShares`) in a ledger entry, not a token. Redeemable NHB is
`shares * SupplyIndex`, and the supply index grows as borrowers pay interest.
The supplier rate follows the utilisation curve described in
[on-chain.md](on-chain.md#interest-rate-model).

### Borrowing NHB against ZNHB
Borrowing is over-collateralised. A borrower deposits ZNHB as collateral
(`TxTypeLendingDepositZNHB`) and then borrows NHB (`TxTypeLendingBorrowNHB`).
Debt accrues interest through a borrow index. The borrow is refused if it would
exceed the maximum loan-to-value, if pool liquidity is short, or if the price
guard rejects the reference price.

### Collateral valuation
ZNHB collateral is valued in NHB using a reference price submitted by signed
`TxTypeLendingRefPrice` transactions. Until the first price is accepted,
collateral is valued 1:1 with NHB. All collateral is ZNHB; there are no
per-asset LTV settings and no per-asset enable/disable switch.

### Liquidation
A position is liquidatable when `collateralValue * LiquidationThreshold <
debt * 10000` (see [on-chain.md](on-chain.md#health-and-borrow-limits)). Any
other account can liquidate by signing a `TxTypeLendingLiquidate` transaction;
the liquidator repays the borrower's whole flexible-rate debt and receives the
seized ZNHB.

### Fixed-term products
Besides the flexible pool, the module has fixed-term loans (30 or 90 days by
default) and fixed-term deposits with locked rates. See
[on-chain.md](on-chain.md#fixed-term-products).

## Key terms

* **LTV / `MaxLTV`**: borrow-time cap, in basis points. `config.toml`: `6000`.
* **Liquidation threshold**: the health limit, in basis points. `config.toml`:
  `8500`.
* **Utilisation**: `TotalNHBBorrowed / TotalNHBSupplied`.
* **Reserve factor**: share of interest routed to `FeeAccrual.ProtocolFeesWei`
  rather than suppliers. `config.toml`: `ReserveFactorBps = 1000`.
* **Pool**: an independent market identified by `poolId`; `default` is
  implicit.

## Lifecycle of a position

1. **Supply / deposit collateral**: `0x13` supplies NHB; `0x15` deposits ZNHB.
2. **Borrow**: `0x17` borrows NHB, subject to the checks above.
3. **Accrual**: indexes advance per block whenever a pool action touches the
   pool.
4. **Repay or adjust**: `0x18` repays; `0x16` withdraws collateral while the
   position stays healthy.
5. **Liquidation**: `0x1D` by a third party once the position is unhealthy.

## Next steps

* [On-Chain Architecture](on-chain.md)
* [RPC and transaction reference](rpc-api.md)
* [Developer Guide](developer-guide.md)
* [State keys and query paths](state-indexes.md)
