# On-Chain Lending Architecture

How the native lending module (`native/lending`, applied by
`core/lending_native.go`) accounts for balances, prices interest, values
collateral and liquidates. Everything here is from the Go source.

## Assets and pools

* Supplied and borrowed asset: `NHB`. Collateral: `ZNHB`. There is no
  per-asset market list. A pool is identified by `poolId`; `default` exists
  implicitly and other pools are created with `TxTypeLendingCreatePool`.
* NHB lives in a module account and ZNHB collateral in a separate collateral
  account (`Node.LendingModuleAddress`, `Node.LendingCollateralAddress`).
* Suppliers hold **shares** in `UserAccount.SupplyShares`; they are ledger
  entries, not transferable tokens. Redeemable NHB is
  `shares * SupplyIndex / 1e27`, rounded half-up.
* Indexes and shares use 1e27 fixed-point ("ray") arithmetic with half-up
  rounding (`native/lending/math.go`).
* The first supply into a pool with zero total shares must be at least 1 NHB
  (`1e18` wei) or it fails with `deposit below minimum liquidity`; a supply
  that would mint zero shares fails the same way.
* A supplier cannot withdraw in the same block as their own supply
  (`cannot withdraw in the same block as a supply`).
* Available liquidity is `TotalNHBSupplied + TotalFixedTermDepositPrincipalWei - TotalNHBBorrowed`,
  floored at zero. Borrows and withdrawals are limited to it.

## Interest rate model

`InterestModel` (`native/lending/interest.go`) with utilisation
`U = TotalNHBBorrowed / TotalNHBSupplied`:

* `U == 0`: borrow APR = `BaseRate`.
* `U <= Kink` (or `Kink == 0`): `BaseRate + Slope1 * U`.
* `U > Kink`: `BaseRate + Slope1 * Kink + Slope2 * (U - Kink)`.

The node uses `DefaultInterestModel`: base 2%, slope 1 15%, slope 2 60%, kink
80% (`NewInterestModel(0.02, 0.15, 0.6, 0.8)`). The model is not read from
config or governance.

Supplier rate is `borrowAPR * U * (1 - (ReserveFactorBps + ProtocolFeeBps) / 10000)`
(the sum is capped at 10000 bps). `config.toml` sets `ReserveFactorBps = 1000`
and `ProtocolFeeBps = 0`.

## Accrual

`accrueInterest` runs at the start of supply, withdraw, collateral
withdrawal, borrow, repay, liquidate and fixed-term repay
(`native/lending/engine.go`, `fixed_term.go`), and interest is measured in
**seconds of block time**, not in blocks:

1. `elapsed = block timestamp - market.LastUpdateTimestamp` (Unix seconds,
   `Market.LastUpdateTimestamp` in `native/lending/types.go`). Nothing accrues
   when `elapsed == 0` (blocks that share a second) or nothing is borrowed. A
   market with no stored timestamp, or an engine that was given no block time,
   accrues once by `(current height - LastUpdateBlock)` times one second per
   block and is then stamped (`Engine.elapsedSeconds`).
2. The per-second rate is `APR / 31,536,000` (`secondsPerYear`, 365 days). The
   borrow and supply indexes grow by `1 + APR / 31,536,000 * elapsed` (linear
   within one step).
3. Interest `= TotalNHBBorrowed * APR / 31,536,000 * elapsed` is added to both
   `TotalNHBBorrowed` and `TotalNHBSupplied`.
4. `ReserveFactorBps` and `ProtocolFeeBps` shares of that interest are added
   to `FeeAccrual.ProtocolFeesWei`.

The block time comes from the block header timestamp the state processor is
given (`core/lending_native.go`), so a year of interest is a year of block time
whatever the block interval is (see
[`docs/consensus/block-cadence.md`](../../consensus/block-cadence.md)). The
read RPCs project accrual to the newest committed block's timestamp.

Borrower debt is stored as `ScaledDebt` (debt divided by the borrow index at
borrow time) and read back as `ScaledDebt * BorrowIndex / 1e27`.

`FeeAccrual` also has `DeveloperFeesWei`, credited by the developer fee
charged at borrow time. `Engine.WithdrawProtocolFees` and
`WithdrawDeveloperFees` exist but have no caller outside the engine's own
tests, so accrued fee balances have no on-chain withdrawal path today.

## Collateral valuation

`OracleAdjustedCollateralValue` (`native/lending/engine.go`):

* If the market has a reference price (`Market.OracleMedianWei > 0`), ZNHB
  collateral value in NHB-wei is `collateral * OracleMedianWei / 1e18`, rounded
  down.
* If it has never received one, collateral is valued **1:1** with NHB.

The reference price is written to every market by `TxTypeLendingRefPrice`
(see [rpc-api.md](rpc-api.md#lending_submitrefprice-jwt-required)). Before a
new median overwrites it, the old one is saved as `OraclePrevMedianWei`.

`guardOracle` runs on borrow, fixed-term borrow (`native/lending/fixed_term.go`),
liquidation, and on collateral withdrawal while the account has debt:

* If `Oracle.MaxAgeBlocks > 0`: a market whose `OracleUpdatedBlock` is `0`
  (never updated) or older than `MaxAgeBlocks` returns `oracle quote stale`.
  With the shipped `OracleMaxAgeBlocks = 1000`, borrowing is blocked until a
  first reference price lands. The age is counted in blocks, so its length in
  time follows the block interval: 1,000 blocks is 2,000 seconds at one block
  every 2 seconds, which is what the shipped `[consensus] MinBlockInterval`
  gives.
* If `Oracle.MaxDeviationBps > 0` and both medians are positive: a move larger
  than that share of the previous median returns `oracle deviation too large`.

## Health and borrow limits

For collateral value `V` (NHB-wei) and debt `D`:

* **Healthy** when `V * LiquidationThreshold >= D * 10000`. A position with no
  debt is healthy; a position with zero collateral value and debt is not.
* **Borrow limit**: a borrow must leave `V * MaxLTV >= D * 10000`, where `D`
  includes the new borrow, the developer fee if any, and the outstanding amount
  of an active fixed-term loan.

The same checks apply to collateral withdrawals (health only) and borrows
(health and max LTV). There is no stored "health factor" number on-chain; the
`lendingd` gateway computes one (see [`docs/lending/service.md`](../../lending/service.md)).

Repay is capped at the current debt and is available while the oracle is
stale; it is blocked only by pauses.

## Liquidation

`Engine.Liquidate(liquidator, borrower)`:

1. Refuses a liquidator that is the borrower (`lending engine: a borrower
   cannot liquidate their own position`; the transaction handler adds `; use
   repay instead`). Requires the borrower to have flexible-rate debt and to be
   **not healthy** (fixed-term debt is not counted for liquidation
   eligibility). Runs `guardOracle` first.
2. The liquidator repays the borrower's **entire** flexible debt in NHB; there
   is no close factor and no partial liquidation.
3. The seized collateral is the bonus-inclusive NHB value of the repaid debt
   converted to ZNHB at the market's reference price
   (`CollateralForDebtValue`): `repayAmount * (10000 + LiquidationBonus) *
   1e18 / (OracleMedianWei * 10000)`, rounded down, and capped at the
   collateral the borrower holds. With no reference price the conversion is
   1:1, `repayAmount * (10000 + LiquidationBonus) / 10000`, the same valuation
   `OracleAdjustedCollateralValue` uses for the eligibility check.
4. The borrower's debt and scaled debt are set to zero.

Each address involved (liquidator, borrower, module account, collateral
account, developer and protocol targets) is loaded once and written once, so
roles that resolve to the same address cannot overwrite each other's balance
changes (`native/lending/accounts.go` for borrow and fee withdrawals, the same
pattern inside `Liquidate`).

The node binaries never set `LiquidationBonus` (see below), so the bonus is
`0`.

### Collateral routing

`[lending.collateralRouting]` (`LiquidatorBps`, `DeveloperBps`,
`DeveloperAddress`, `ProtocolBps`, `ProtocolAddress`) splits seized collateral.
Developer and protocol shares are `seized * bps / 10000`; the liquidator gets
the rest. The sum of the three basis points must not exceed 10000 (startup
panics otherwise, `cmd/nhb/main.go`); a non-zero developer or protocol share
requires its address (startup panics otherwise). The shipped `config.toml`
sets all three shares to `0`, so the liquidator receives everything.

## Developer fee

`DeveloperFeeBps` and `DeveloperFeeCollector` in `[lending]` are copied into
each pool at creation. When a pool has a non-zero developer fee, the engine
charges it on every borrow: the borrower receives `amount`, the collector
receives `amount * bps / 10000`, and both are added to the borrower's debt.
`useDeveloperFee: true` in the borrow payload additionally requires the pool to
have a fee and collector configured, otherwise the borrow fails with
`developer fee disabled`. The fee must not exceed `DeveloperFeeCapBps`, which
the node sets equal to `DeveloperFeeBps`. `config.toml` sets `DeveloperFeeBps = 0`.
A non-zero `DeveloperFeeBps` with an empty collector makes the node panic at
startup.

## Risk parameters

`RiskParameters` is populated at startup from `[lending]` in `config.toml`
(`cmd/nhb/main.go`):

| Config key | Field | Shipped value |
| --- | --- | --- |
| `MaxLTVBps` | `MaxLTV` | `6000` |
| `LiquidationThresholdBps` | `LiquidationThreshold` | `8500` |
| `DeveloperFeeBps` | `DeveloperFeeCapBps` | `0` |
| `OracleMaxAgeBlocks` | `Oracle.MaxAgeBlocks` | `1000` (`0` disables) |
| `OracleMaxDeviationBps` | `Oracle.MaxDeviationBps` | `5000` (`0` disables) |
| `ReserveFactorBps`, `ProtocolFeeBps` | accrual split | `1000`, `0` |

`LiquidationBonus`, `BorrowCaps`, `CircuitBreakerActive`, `OracleAddress` and
the per-action `Pauses` exist in the struct but no config key or governance
proposal sets them; they stay at zero or `false`. The `[lending.breaker]`
section (`MaxTotalSupplyWei`, `MaxTotalBorrowWei`, `MaxTotalCollateralWei`) is
parsed into `lending.Config` and is not passed to the engine. The `lending`
module-wide pause is the only live pause.

These values are fixed per node from local config. Governance can currently
change only the fixed-term rate schedules (`policy.lendingRateSchedule`,
`policy.lendingDepositRateSchedule`). Every validator must run the same config.

## Treasury wallet

`TxTypeLendingDepositZNHB`, `TxTypeLendingWithdrawZNHB` and
`TxTypeLendingLiquidate` are among the transaction types whose net ZNHB
movement onto or off the admin/treasury wallet is booked into the ZNHB Reward
Pool ledger in the same state transition (`treasuryZNHBFlowTracked`,
`core/znhb_treasury_pool.go`). When that wallet is the depositor, withdrawer or
liquidator and an outflow is larger than the Reward Pool holds, the transaction
is refused with `znhb: treasury reward pool cannot cover this outflow`.

## Fixed-term products

Separate state from the flexible ledger (`native/lending/fixed_term.go`,
`fixed_term_deposit.go`, `autodebit.go`, `deposit_payout.go`):

* **Fixed-term loan**: one active loan per borrower per pool. Interest is
  `principal * rateBps / 10000` for the whole tenure regardless of when it is
  repaid. Default schedule: 30 days at 1200 bps, 90 days at 1600 bps.
* **Auto-debit**: interest is collected in 30-day cycles, dated by block
  timestamp (a 30-day loan has one
  cycle, a 90-day loan three). Three consecutive missed debits mark the loan
  `delinquent`; no collateral is seized by that transition.
* **Fixed-term deposit**: locked rate and tenure; payout is either
  `lump_sum_at_maturity` or `periodic_interest_principal_at_maturity`. New
  deposits are capped by the pool's aggregate fixed-term loan interest
  receivable (`fixed-term deposit would exceed the pool's fixed-term loan
  interest capacity`).

## Events

The lending code emits these event types (`core/events/lending_*.go`):

* `lending.refprice.recorded`
* `lending.autodebit.succeeded`, `lending.autodebit.failed`,
  `lending.fixedterm.delinquent`
* `lending.depositpayout.succeeded`, `lending.depositpayout.delayed`

`core/lending_native.go` and `native/lending/engine.go` do not append events
for supply, withdraw, collateral, borrow, repay or liquidate. Read state
through the RPC methods in [rpc-api.md](rpc-api.md) or the query router in
[state-indexes.md](state-indexes.md).
