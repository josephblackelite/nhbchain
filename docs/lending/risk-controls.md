# Lending Risk Controls

Guardrails the lending engine enforces (`native/lending/engine.go`), and which
of them can actually be configured on a running node.

## Fixed-point precision and minimum liquidity

Interest indexes and share amounts use 1e27 fixed-point arithmetic with
half-up rounding (`native/lending/math.go`). The first supply into a pool that
has zero total shares must be at least 1 NHB (`1e18` wei); smaller deposits are
rejected with `deposit below minimum liquidity`, as is any supply that would
mint zero shares. A supplier also cannot withdraw in the same block as their own
supply.

## Loan-to-value and health

* Borrow limit: `collateralValue * MaxLTV >= debt * 10000` after the borrow
  (`lending engine: borrow would exceed maximum loan-to-value ratio`).
* Health: `collateralValue * LiquidationThreshold >= debt * 10000`
  (`lending engine: borrower health factor below 1`). Applied to borrows and
  collateral withdrawals.
* Debt for these checks includes an active fixed-term loan's outstanding
  amount.
* `config.toml` `[lending]`: `MaxLTVBps = 6000`, `LiquidationThresholdBps = 8500`.

## Oracle freshness and deviation

`guardOracle` protects borrows (including fixed-term borrows,
`native/lending/fixed_term.go`), liquidations, and collateral withdrawals made
while the account has debt:

* `OracleMaxAgeBlocks` (`config.toml`: `1000`; `0` disables): the market's last
  reference-price update (`OracleUpdatedBlock`) must be at most this many blocks
  old, and a market that has never had one is treated as stale.
* `OracleMaxDeviationBps` (`config.toml`: `5000`; `0` disables): the latest
  median must be within this share of the previous median.

Repayment is not gated by the oracle. Reference prices come from
`TxTypeLendingRefPrice`; see [`docs/finance/lending/rpc-api.md`](../finance/lending/rpc-api.md#lending_submitrefprice-jwt-required).

## Fields that exist but are not configurable

`RiskParameters` (`native/lending/types.go`, `params.go`) also defines
`BorrowCaps` (`PerBlock`, `Total`, `UtilisationBps`), per-action `Pauses`
(`Supply`, `Borrow`, `Repay`, `Liquidate`), `CircuitBreakerActive` and
`LiquidationBonus`. The engine enforces them when set, but `cmd/nhb/main.go`
and `cmd/consensusd/main.go` build `RiskParameters` from `[lending]` with only
`MaxLTV`, `LiquidationThreshold`, `DeveloperFeeCapBps` and `Oracle`, and no
governance proposal changes them. On a running node they are zero/`false`:

* there are no borrow caps and no action-level pauses;
* `LiquidationBonus` is `0`;
* `[lending.breaker]` (`MaxTotalSupplyWei`, `MaxTotalBorrowWei`,
  `MaxTotalCollateralWei`) is parsed by the config loader but not passed to the
  engine.

## Module pause

The `lending` entry in the module pause map (`config.Pauses.Lending`) makes
every engine action return `ErrModulePaused`. Inspect and change it with:

```bash
go run ./examples/docs/ops/read_pauses --db ./nhb-data --consensus localhost:9090
go run ./examples/docs/ops/pause_toggle --module lending --state pause --authority <governance authority address>
```

(`pause_toggle` also accepts `--db`, `--consensus` and `--governance`; see
`examples/docs/ops/pause_toggle/main.go`.)

## Governed parameters

Only the fixed-term rate schedules are changed on-chain:
`policy.lendingRateSchedule` (borrow side) and
`policy.lendingDepositRateSchedule` (deposit side)
(`native/governance/types.go`). Read the borrow schedule with
`lending_getRateSchedule`.
