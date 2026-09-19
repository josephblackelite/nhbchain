# Lending RPC and Transaction Reference

Lending state is read through JSON-RPC methods on the node and written by
**signed transactions** submitted with `nhb_sendTransaction`. The old
per-action RPC methods are disabled.

All requests use `"jsonrpc": "2.0"`. Amount fields in results are decimal wei
strings unless noted.

## Authentication

The read methods below have no authentication check in the dispatch switch
(`rpc/http.go`). `lending_submitRefPrice` and `nhb_sendTransaction` require the
RPC credential (`requireAuthInto`): a JWT sent as
`Authorization: Bearer <jwt>` (validated per `[RPCJWT]` in `config.toml`) or a
verified client certificate when required. `nhb-cli` reads the token it sends
from `NHB_RPC_TOKEN`; the node does not read that variable.

## Disabled and removed methods

* `lending_supplyNHB`, `lending_withdrawNHB`, `lending_depositZNHB`,
  `lending_withdrawZNHB`, `lending_borrowNHB`, `lending_borrowNHBWithFee`,
  `lending_repayNHB`, `lending_liquidate` are registered but always return
  HTTP `410` with JSON-RPC code `-32060` (`codeMethodDisabled`) and a message
  pointing to signed transactions (`rpc/lending_handlers.go`
  `lendingRPCDisabledMessage`). The reason recorded in the code is that they
  mutated any address's position without a signature from that address.
* `lend_createPool` no longer exists; unknown methods return
  `codeMethodNotFound` (`-32601`). Pool creation is `TxTypeLendingCreatePool`.

## Read methods

### `lending_getMarket`

Params: none, a pool ID string, or `{"poolId": "..."}`. An empty pool ID means
`default`; more than one parameter is an error.

Result: `{"market": {...}, "riskParameters": {...}}`. `market` is omitted when
the pool does not exist and is not the default pool.

`market` is `lending.Market` (`native/lending/types.go`). Only three fields
have JSON tags, so the rest use Go field names, and `*big.Int` fields are
rendered as JSON numbers, not strings:

`PoolID`, `DeveloperOwner`, `DeveloperFeeCollector`, `DeveloperFeeBps`,
`TotalNHBSupplied`, `TotalSupplyShares`, `TotalNHBBorrowed`, `SupplyIndex`,
`BorrowIndex`, `LastUpdateBlock`, `ReserveFactor`, `BorrowedThisBlock`,
`LastBorrowBlock`, `OracleMedianWei`, `OraclePrevMedianWei`,
`OracleUpdatedBlock`, `TotalFixedTermDepositPrincipalWei`,
`TotalFixedTermDepositInterestOwedWei`, `FixedTermDepositReserveWei`,
`TotalFixedTermLoanInterestReceivableWei`, plus the tagged `depositApyBps`,
`borrowApyBps`, `availableLiquidityWei`.

The handler projects interest accrual to the current height before returning,
and computes `depositApyBps`, `borrowApyBps` and `availableLiquidityWei` on the
fly; they are not stored.

`riskParameters` is `lending.RiskParameters`, also without JSON tags:
`MaxLTV`, `LiquidationThreshold`, `LiquidationBonus`, `OracleAddress`,
`CircuitBreakerActive`, `DeveloperFeeCapBps`, `BorrowCaps`
(`PerBlock`, `Total`, `UtilisationBps`), `Oracle` (`MaxAgeBlocks`,
`MaxDeviationBps`), `Pauses` (`Supply`, `Borrow`, `Repay`, `Liquidate`). The
node binaries fill in only `MaxLTV`, `LiquidationThreshold`,
`DeveloperFeeCapBps` and `Oracle` from config ([on-chain.md](on-chain.md#risk-parameters)),
so the other fields read as zero or `false`.

### `lend_getPools`

No parameters. Result: `{"pools": [<market>...], "riskParameters": {...}}`,
same shapes as above. When no pool is stored, a single default market is
returned.

### `lending_getUserAccount`

Params: one argument, either the bech32 address string or
`{"address": "...", "poolId": "..."}`; pool defaults to `default`. Returns HTTP
`404` (`account not found`) when the address has no record in that pool.

Result:

```json
{
  "account": {
    "address": "0x<20-byte hex>",
    "supplied":  [{"poolId": "default", "amountWei": "...", "valueUsd": "..."}],
    "borrowed":  [{"poolId": "default", "amountWei": "...", "valueUsd": "..."}],
    "collateralZnhbWei": "...",
    "collateralValueUsd": "...",
    "borrowedValueUsd": "...",
    "rewardsWei": "0"
  }
}
```

* `supplied[].amountWei` is the redeemable NHB (shares times the supply
  index), not the share count. `borrowed[]` lists only the flexible-rate debt.
* `collateralValueUsd` uses the same oracle-adjusted conversion the engine
  enforces (`lending.OracleAdjustedCollateralValue`); it is `""` when the
  market has no reference price. `valueUsd` values treat NHB as 1 USD.
* `borrowedValueUsd` includes the outstanding amount of an active fixed-term
  loan; the `borrowed` array does not.
* `rewardsWei` is always `"0"`; the engine has no rewards accrual.

### `lending_getFixedTermLoan`

Params as for `lending_getUserAccount`. Result: `{"loan": null}` when the
address has no active fixed-term loan in the pool, otherwise `loan` with
`loanId`, `borrower`, `poolId`, `tenureDays`, `rateBps`, `principalWei`,
`totalInterestWei`, `repaidWei`, `outstandingWei`, `issuedAtBlock`,
`issuedAtTime`, `maturityTime`, `status` (`active`, `repaid`, `delinquent`),
`autoDebitEnabled`, `nextAutoDebitCycle`, `totalAutoDebitCycles`,
`consecutiveMissedAutoDebits`.

### `lending_getFixedTermDeposit`

Param: a 32-byte hex deposit ID (with or without `0x`), or
`{"depositId": "..."}`. Result: `{"deposit": null}` or `deposit` with
`depositId`, `depositor`, `poolId`, `tenureDays`, `rateBps`, `principalWei`,
`totalInterestOwedWei`, `paidInterestWei`, `outstandingInterestWei`, `payout`
(`lump_sum_at_maturity` or `periodic_interest_principal_at_maturity`),
`issuedAtBlock`, `issuedAtTime`, `maturityTime`, `status` (`active`,
`matured`), `nextPayoutCycle`.

### `lending_getRateSchedule`

No parameters. Result: `{"schedule": [{"tenureDays": 30, "rateBps": 1200}, ...]}`
sorted by tenure. This is the effective fixed-term **borrow** schedule: the
governed value if a `policy.lendingRateSchedule` proposal has executed,
otherwise the built-in default `{30: 1200, 90: 1600}`.

### `lending_getRefPriceStatus`

No parameters. Result: `{"hasRefPrice": bool, "rateNum", "rateDenom",
"timestamp", "signerCount", "appliedBlock", "marketCount"}`; everything except
`hasRefPrice` is omitted when no price has been accepted.

### `lending_submitRefPrice` (JWT required)

Params: one object `{"rateNum": "...", "rateDenom": "...", "timestamp": <unix>,
"signatures": ["0x..."]}`. `rateNum` and `rateDenom` are positive decimal
integer strings; at least one 65-byte signature is required. Result:
`{"txHash": "0x..."}`. The node enqueues a `TxTypeLendingRefPrice` (`0x26`)
transaction.

The signed message is
`NHB_LENDING_REFPRICE_V1|rate=<rate with 18 decimals>|ts=<timestamp>`; the
digest is its keccak256 (`core/tokenomics/lendingoracle`). On execution the
signatures must come from distinct addresses in the configured reference-price
signer set (the same signer set and threshold the buyback config carries,
`sp.buybackConfig`), meeting the threshold; the timestamp must be strictly
newer than the last accepted one. The rate is stored on every market as
`OracleMedianWei = rateNum * 1e18 / rateDenom` (NHB-wei per whole ZNHB).

## Write transactions

Submit with `nhb_sendTransaction`. The transaction `type` selects the action,
`value` carries the amount in wei, and `data` (base64 in the JSON transaction)
is a UTF-8 JSON payload. The sender is the recovered signer; there is no
caller-supplied address field. Each transaction increments the sender's nonce.

| Action | Type | `value` | `data` JSON |
| --- | --- | --- | --- |
| Create pool | `TxTypeLendingCreatePool` `0x2C` | not used | `{"poolId": "<non-default id>"}` |
| Supply NHB | `TxTypeLendingSupplyNHB` `0x13` | NHB to supply | `{"poolId"?}` |
| Withdraw NHB | `TxTypeLendingWithdrawNHB` `0x14` | **LP shares** to burn | `{"poolId"?}` |
| Deposit ZNHB collateral | `TxTypeLendingDepositZNHB` `0x15` | ZNHB | `{"poolId"?}` |
| Withdraw ZNHB collateral | `TxTypeLendingWithdrawZNHB` `0x16` | ZNHB | `{"poolId"?}` |
| Borrow NHB | `TxTypeLendingBorrowNHB` `0x17` | NHB to borrow | `{"poolId"?, "useDeveloperFee"?}` |
| Repay NHB | `TxTypeLendingRepayNHB` `0x18` | NHB (capped at current debt) | `{"poolId"?}` |
| Liquidate | `TxTypeLendingLiquidate` `0x1D` | not used | `{"poolId"?, "borrower": "<bech32>"}` |
| Fixed-term borrow | `TxTypeLendingBorrowFixedTerm` `0x38` | principal | `{"poolId"?, "tenureDays": 30 or 90}` |
| Fixed-term repay | `TxTypeLendingRepayFixedTerm` `0x39` | amount | `{"poolId"?}` |
| Fixed-term supply | `TxTypeLendingSupplyFixedTerm` `0x3A` | principal | `{"poolId"?, "tenureDays", "payout": "lump_sum_at_maturity" or "periodic_interest_principal_at_maturity"}` |

Rules from `core/lending_native.go`:

* An omitted or empty `poolId` means `default`. A create-pool transaction must
  name a pool other than `default`, and the pool's developer owner is always
  the transaction signer.
* `value` must be positive for every action except liquidate.
* A borrower cannot liquidate their own position.
* Fixed-term borrow requires a non-zero `tenureDays` that is in the effective
  rate schedule; fixed-term supply also requires a valid `payout`.
* Every lending transaction is counted against the `lending` module quota
  (`applyQuota`). The engine actions (everything except create pool) are also
  rejected while the `lending` module is paused.

The transaction hash returned by `nhb_sendTransaction` is the mempool
acknowledgement; the action takes effect when a block includes it.

## Errors

Engine errors keep the `lending engine:` prefix. Those the code defines include
`lending engine: amount must be positive`, `insufficient balance`,
`insufficient liquidity`, `borrower health factor below 1`,
`no outstanding debt to repay`, `borrower not eligible for liquidation`,
`deposit below minimum liquidity`, `oracle quote stale`,
`oracle deviation too large`, `borrow would exceed maximum loan-to-value ratio`,
`cannot withdraw in the same block as a supply`, and
`supply operations paused` / `borrow operations paused` /
`repay operations paused` / `liquidation operations paused`
(`native/lending/engine.go` lines 15-46).
