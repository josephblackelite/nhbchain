# Governance Parameters

Reference for the keys a `param.update` (or `param.emergency_override` /
`param.update_fee_rate`) proposal can set, and for the keys written by the
dedicated proposal kinds. Everything here is taken from
`native/governance/engine.go` (`validatorForParam`), `native/governance/types.go`
(key constants), `config/config.go` (`defaultAllowedGovernanceParams`) and the
code that reads each key back.

## How a parameter proposal is checked

`Engine.validateParamPayload` (`native/governance/engine.go`):

- the payload must be a JSON object with at least one key; a single top-level
  key named `update`, `parameter` or `params` whose value is an object is
  unwrapped;
- every key must be in the node's `AllowedParams` (`governance: parameter
  "<key>" not in allow-list`);
- every key must have a validator in `validatorForParam` (`governance:
  parameter "<key>" missing validation rule`), and the value must pass it.

On execution the raw JSON text of each value is written to the param store
(`ParamStoreSet(key, raw)`), exactly as submitted, so a quoted string is stored
with its quotes and a leading `+` is stored as written. Validators accept an
integer either as a JSON number or as a decimal string (one leading `+` is
tolerated; decimals, exponents and negative numbers are rejected), and every
reader that consumes a governed numeric value first passes the stored text
through `nativecommon.ParamDecimal` (`native/common/paramvalue.go`), which trims
whitespace, removes the JSON quotes when the whole value is a JSON string and
drops leading `+` characters. `1250` and `"1250"` therefore behave identically.
Booleans and the reward asset go through `ParamText`, which does the same
without the `+` handling. The readers that use these helpers are the staking
reward engine (`core/state/staking_rewards.go`), `Node.SyncStakingParams`, the
staking payout, unbonding and emission-cap readers and the mint cap reader in
`core/state_transition.go`, `MinimumValidatorStakeFromParam`
(`native/governance/types.go`), the escrow realm bounds
(`native/escrow/engine.go`), and the market flat fee and paymaster top-up fee
readers (`core/swap_risk_params.go`, `rpc/market_handlers.go`). Queries that
return stored values (`QueryState("gov", "params")`) return the text as stored,
quotes included. `uint64` values are limited to `2^53 - 1`.

A stored value that a reader cannot parse cannot be produced by these
validators. If it happens anyway, `Node.SyncStakingParams` keeps the configured
value and `MinimumValidatorStakeFromParam` falls back to the default, both
logging `governed parameter has a malformed stored value`
(`nativecommon.ReportMalformedParam`); the payout-period, unbonding-period and
emission-cap readers in `core/state_transition.go` return an error instead.

## Default `AllowedParams`

`AllowedParams` in the node's `[governance]` block replaces the default list
when it is non-empty. The default list (`config/config.go:25`) is:

`fees.baseFee`, `staking.minimumValidatorStake`, `staking.aprBps`,
`staking.payoutPeriodDays`, `staking.unbondingDays`, `staking.minStakeWei`,
`staking.maxEmissionPerYearWei`, `mint.nhb.maxEmissionPerYearWei`,
`mint.znhb.maxEmissionPerYearWei`, `staking.rewardAsset`,
`staking.compoundDefault`, the fourteen `loyalty.dynamic.*` keys below,
`network.seeds`, `potso.abuse.MaxUserShareBps`, `potso.abuse.MinStakeToEarnWei`,
`potso.abuse.QuadraticTxDampenAfter`, `potso.abuse.QuadraticTxDampenPower`,
`potso.rewards.EmissionPerEpochWei`, `potso.weights.AlphaStakeBps`,
`market.flatFeeWei`, `paymaster.topUpFeeWei`, and the ten keys listed under
[Allowed by default but not settable](#allowed-by-default-but-not-settable).

The repository's root `config.toml` sets its own `AllowedParams` (it does not
include the `mint.*` or `paymaster.topUpFeeWei` keys).

## Keys with a validator

"Read by" names the code that uses the stored value. "No reader found" means a
search of the repository found no code that reads the key from the param
store, so setting it has no effect on chain behavior.

| Key | Validation | Default (when unset) | Read by |
| --- | --- | --- | --- |
| `staking.minimumValidatorStake` | positive integer (wei) | `10000000000000000000000` (10,000 ZNHB), `governance.DefaultMinimumValidatorStake` | `Manager.MinimumValidatorStake`, `StateProcessor.minimumValidatorStake` (validator eligibility) |
| `staking.aprBps` | integer `0`-`10000` | `1250` (`defaultGlobalConfig`) | `core/state/staking_rewards.go`, `Node.SyncStakingParams` |
| `staking.payoutPeriodDays` | integer `>= 1` (no upper bound in the validator) | `30` | `core/state/staking_rewards.go`, `core/state_transition.go` |
| `staking.unbondingDays` | integer `>= 1` (no upper bound) | `7` | `core/state_transition.go` |
| `staking.minStakeWei` | non-negative integer | `10000000000000000000000` | Merged into the node's staking config by `SyncStakingParams`; the constant's own doc comment in `types.go` states it has no enforcement path in the delegation handler and is not the validator-eligibility gate |
| `staking.maxEmissionPerYearWei` | integer `>= 0` | `5000000000000000000` | `core/state/staking_rewards.go`, `core/state_transition.go` |
| `staking.rewardAsset` | non-empty JSON string | `ZNHB` | Merged into the node's staking config by `SyncStakingParams` |
| `staking.compoundDefault` | boolean (or the strings `true`/`false`) | `false` | Merged into the node's staking config by `SyncStakingParams` |
| `mint.nhb.maxEmissionPerYearWei` | integer `>= 0` | none (`0` or unset means no cap) | `applyMintTransaction` (`core/state_transition.go`): a mint that would push the calendar-year NHB total over a positive cap fails with `ErrMintEmissionCapExceeded` |
| `mint.znhb.maxEmissionPerYearWei` | integer `>= 0` | none | Only reachable from the ZNHB branch of `applyMintTransaction`, which is preceded by an unconditional `ErrMintZNHBNotMintable` rejection |
| `market.flatFeeWei` | non-negative integer | `100000000000000000` (0.1 NHB, `defaultMarketFlatFeeWei` in `core/market_native.go`) | `core/market_native.go` (`readGovernedMarketFlatFeeWei`, the buyer-side flat fee on a listing fill), `rpc/market_handlers.go` |
| `paymaster.topUpFeeWei` | non-negative integer | `0` (no fee) | `core/sponsorship.go` (`readGovernedPaymasterTopUpFeeWei`); the fee is in the top-up asset, which is NHB unless the top-up policy names ZNHB (`paymasterSponsoredAsset`) |
| `network.seeds` | non-empty, must parse with `seeds.Parse` | none | `core/node.go` (`ParamStoreGet("network.seeds")`), `cmd/p2pd/main.go` |
| `fees.baseFee` | integer `<= 1000000000000000` (1e15) | none | No reader found |
| `potso.weights.AlphaStakeBps` | integer `<= 10000` | none | No reader found |
| `potso.rewards.EmissionPerEpochWei` | integer `<= 9223372036854775807` | none | No reader found |
| `potso.abuse.MaxUserShareBps` | integer `<= 10000` | none | No reader found |
| `potso.abuse.MinStakeToEarnWei` | integer `>= 0` | none | No reader found |
| `potso.abuse.QuadraticTxDampenAfter` | unsigned integer | none | No reader found |
| `potso.abuse.QuadraticTxDampenPower` | integer, not `1` (`0` disables, otherwise `>= 2`) | none | No reader found |
| `loyalty.dynamic.targetBps`, `.minBps`, `.maxBps` | integer `<= 10000` | none | No reader found |
| `loyalty.dynamic.smoothingStepBps` | integer `1`-`10000` | none | No reader found |
| `loyalty.dynamic.coverageMax` | number `0`-`1` | none | No reader found |
| `loyalty.dynamic.coverageLookbackDays` | integer `>= 1` | none | No reader found |
| `loyalty.dynamic.dailyCapPctOf7dFees` | number `0`-`1` | none | No reader found |
| `loyalty.dynamic.dailyCapUsd` | number `>= 0` | none | No reader found |
| `loyalty.dynamic.yearlyCapPctOfInitialSupply` | number `0`-`100` | none | No reader found |
| `loyalty.dynamic.priceGuard.pricePair` | non-empty string | none | No reader found |
| `loyalty.dynamic.priceGuard.twapWindowSeconds`, `.priceMaxAgeSeconds` | integer `>= 1` | none | No reader found |
| `loyalty.dynamic.priceGuard.maxDeviationBps` | integer `<= 10000` | none | No reader found |
| `loyalty.dynamic.priceGuard.enabled` | boolean | none | No reader found |
| `slashing.policy.enabled` | boolean | none | No reader found |
| `slashing.policy.maxPenaltyBps` | integer `<= 10000` | none | No reader found |
| `slashing.policy.windowSeconds` | integer `60`-`2592000` | none | No reader found |
| `slashing.policy.evidenceTtlSeconds` | integer `60`-`7776000` | none | No reader found |
| `slashing.policy.maxSlashWei` | integer `<= 9223372036854775807` | none | No reader found |
| `protocol.feeRateBps` | integer `<= 10000` | none | No reader found; not in the default `AllowedParams` |
| `escrow.realm.MinThreshold`, `escrow.realm.MaxThreshold` | integer `1`-`100` | none | `native/escrow/engine.go`; not in the default `AllowedParams` |
| `escrow.realm.AllowedSchemes` | non-empty array (or single string) of `single`, `committee` or a numeric scheme id | none | `native/escrow/engine.go`; not in the default `AllowedParams` |
| `gov.deposit.MinProposalDeposit` | non-negative integer | none | No reader found; not in the default `AllowedParams` |
| `gov.tally.QuorumBps` | integer `<= 10000` | none | No reader found (the running quorum comes from `[governance] QuorumBps`); not in the default `AllowedParams` |
| `gov.tally.ThresholdBps` | integer `5000`-`10000` | none | No reader found (the running threshold comes from `[governance] PassThresholdBps`); not in the default `AllowedParams` |
| `gov.timelock.DurationSeconds` | integer `3600`-`2592000` | none | No reader found (the running timelock comes from `[governance] TimelockSeconds`); not in the default `AllowedParams` |

The fourteen `loyalty.dynamic.*` keys are `targetBps`, `minBps`, `maxBps`,
`smoothingStepBps`, `coverageMax`, `coverageLookbackDays`,
`dailyCapPctOf7dFees`, `dailyCapUsd`, `yearlyCapPctOfInitialSupply`,
`priceGuard.pricePair`, `priceGuard.twapWindowSeconds`,
`priceGuard.priceMaxAgeSeconds`, `priceGuard.maxDeviationBps` and
`priceGuard.enabled`. The loyalty dynamic engine reads its configuration through
`StateProcessor.LoyaltyGlobalConfig` (a record in the state trie,
`core/state_transition.go`), not from these param keys.

`staking.minimumValidatorStake` is the validator-eligibility threshold
(`StateProcessor.setAccount`, `core/state_transition.go`). An address becomes a
validator candidate when it is registered, is not delegating its own stake to a
different validator, and its total `Stake` (which includes ZNHB delegated in by
other wallets, see `validatorEligibilityBasis`) is at least the threshold.

## Allowed by default but not settable

These keys are in `defaultAllowedGovernanceParams` but `validatorForParam` has
no case for them, so a proposal that names one fails at submission with
`missing validation rule`:

`swap.VelocityWindowSeconds`, `swap.VelocityMaxMints`,
`swap.cashOut.assetMonthlyCapWei`, `lending.MaxLTVBps`,
`lending.LiquidationThresholdBps`, `lending.ReserveFactorBps`,
`lending.ProtocolFeeBps`, `lending.breaker.MaxTotalSupplyWei`,
`lending.breaker.MaxTotalBorrowWei`, `lending.breaker.MaxTotalCollateralWei`.

## Keys written by dedicated proposal kinds

These do not use `AllowedParams` (see [proposal types](../gov/proposal-types.md)).

| Kind | Param-store keys written | Value when never set | Read by |
| --- | --- | --- | --- |
| `policy.buybackParams` | `buyback.feeShareBps`, `buyback.discountBps`, `buyback.safetyMarginBps` | Genesis defaults `2000`, `500`, `500` (`core/node.go`, only when a genesis buyback signer quorum exists) | `core/buyback_settlement.go` (`effectiveBuybackConfig`), `core/state_transition.go` (`applyTransactionFee`) |
| `policy.swapRiskParams` | `swap.risk.redeem.perTxMinWei`, `.perTxMaxWei`, `.perAddressDailyCapWei`, `.perAddressMonthlyCapWei` | `5e18`, `1e21`, `2e21`, `2e22` wei (`native/swap/redeem_risk.go`) | `core/swap_risk_params.go` |
| `policy.redemptionFeeParams` | `swap.redemption.feeBps`, `.feeFloorWei`, `.feeCapWei` | `100` bps, `1e18`, `1e21` wei (`native/swap/redemption_fee.go`) | `core/swap_risk_params.go` |
| `policy.lendingRateSchedule` | `lending.fixedTerm.rateSchedule` (one JSON blob) | `{30 days: 1200 bps, 90 days: 1600 bps}` (`native/lending/fixed_term.go`) | `core/lending_rate_schedule.go` |
| `policy.lendingDepositRateSchedule` | `lending.fixedTerm.depositRateSchedule` (one JSON blob) | Empty: no tenure is deposit-eligible until a proposal sets one | `core/lending_rate_schedule.go` |
| `policy.slashing` | `slashing.policy.enabled`, `.maxPenaltyBps`, `.windowSeconds`, `.evidenceTtlSeconds`, `.maxSlashWei` | none | No reader found |

`policy.swapPriceSigner` writes the swap price-signer registry
(`SwapSetPriceSigner` / `SwapClearPriceSigner`), and `role.allowlist` writes
role membership; neither uses the param store.

## Not governable

`rewards.HalvingScheduleConfig(2000, 5000, 3000, 2000)` in `core/node.go` (the
20/50/30 validator/staker/engagement split) and the buyback reference-price
signer quorum (`genesis.BuybackSignerConfig`) are not reachable by any
proposal kind.
