# Staking Emission Operations

This runbook covers tracking staking reward emissions and responding to the annual cap on ZapNHB reward payouts. The behaviour it describes is in `core/state_transition.go` (`StakeClaimRewards`, `stakingMaxEmissionPerYear`); see [`docs/staking/staking.md`](../staking/staking.md) for the full staking reference.

## Monitor year-to-date emissions

* The cumulative amount paid out as staking rewards in a calendar year is stored as a big integer under the state key `staking/ytdEmissions/<YYYY>` (`StakingEmissionYTDKey`, `core/state/manager.go`, with the year zero-padded to four digits). It is read with `Manager.StakingEmissionYTD(year)`. No command shipped in this repository prints it; `nhbctl` has only the `migrate-keystore` command.
* The value is updated on every reward claim that pays out, so an increase without a matching `stake.rewardsClaimed` event indicates a bug that should be escalated.
* A new calendar year uses a new key automatically (the year is taken from the claim's block time, UTC). No manual reset is required.
* The Grafana dashboard `observability/grafana/staking.json` ("Staking Health") has the panels "Rewards Paid per Day", "Total Staked ZNHB", "Staking Pause Status" and "Emission Cap Hits".

## The emission cap

* The cap is the governance parameter `staking.maxEmissionPerYearWei`. It applies only when it is set to a value greater than zero; unset or zero means no cap. A reward claim that would take the year's total above the cap is reduced to the remaining headroom (possibly zero), and the node emits a `stake.emissionCapHit` event.
* The event attributes are `requestedZNHB`, `attemptedZNHB` (the same value), `allowedZNHB`, `ytd` and `cap` (`core/events/stake.go`). Set up alerts on this event so the on-call operator is notified.
* The node does not mint the reward. It debits the configured POTSO reward treasury account (`PotsoRewardConfig().TreasuryAddress`) and credits the claimer, and fails the claim with `staking rewards: treasury not configured` or an insufficient-treasury error when the treasury cannot pay. When the treasury is the network's admin/treasury wallet, that movement is booked into the ZNHB Reward Pool in the same transaction (`treasuryZNHBFlowTracked` lists `TxTypeStakeClaimRewards`, `core/znhb_treasury_pool.go`); a payout the Reward Pool cannot cover fails with `znhb: treasury reward pool cannot cover this outflow`.
* A governed numeric value may be stored bare (`5000000000000000000`) or as a quoted decimal string (`"5000000000000000000"`); the readers accept both and drop a leading `+` (`ParamDecimal`, `native/common/paramvalue.go`).

## Pause behaviour

* While the `staking` module is paused, delegate, undelegate, unbond-claim and reward-claim transactions fail with `staking: module paused` and each emits a `stake.paused` event. The event is kept even though the transaction fails (`executeTransaction` does not discard it). Its attributes are `addr` (the account), `operation` (`delegate`, `undelegate`, `claim` or `claimRewards`), `reason` (`paused by governance`) and, for unbond claims, `unbondingId` (`core/events/stake.go`).
* The read methods `stake_previewClaim` and `stake_getPosition` need RPC authentication and answer HTTP `503`, code `-32050` and `staking module paused` while the pause is active (`rpc/stake_handlers.go`).
* The RPC methods `stake_delegate`, `stake_undelegate`, `stake_claim` and `stake_claimRewards` answer HTTP 410 with code `-32060`; staking actions are signed transactions sent with `nhb_sendTransaction` (`rpc/stake_handlers.go`).
* The pause that is enforced is the on-chain value under `system/pauses` (see [Pause and quota operations](./pause-and-quotas.md)). `Global.Pauses.Staking` in the node configuration is applied at start and replaced by the on-chain value at the next block. If the configuration says unpaused but the on-chain value is paused, `nhb` and `consensusd` log a warning at startup and enforce the on-chain value.

## Unbond claims

A claim of a matured unbonding entry emits `stake.unbondClaimed` with `delegator`, `validator`, `amount` and `unbondingId`. The event name `stake.claimed` belongs only to the legacy alias of the rewards-claim event (`core/events/stake.go`), so an indexer that treats `stake.claimed` as an unbond claim is reading the wrong event.

## When staking is paused

1. Confirm the pause from the `stake.paused` events and the on-chain value (`go run ./examples/docs/ops/read_pauses` prints `staking = true` or `false`).
2. Tell delegators which operations are affected and the expected timeline.
3. Stop automation that retries failed staking transactions.
4. Track pending unbonds approaching maturity so you can tell the affected delegators when the pause lifts.

No transaction path in this repository changes `system/pauses` on a running chain (see "Changing pauses" in [Pause and quota operations](./pause-and-quotas.md)). The helper `examples/docs/ops/pause_toggle` accepts `--module staking`, but the `gov.v1.Pauses` message it sends has no staking field and the node does not accept the message type, so it does not work as a way to pause or resume staking.

## When the emission cap is hit

1. Read the year-to-date total and look at recent `stake.emissionCapHit` events.
2. Decide whether to raise `staking.maxEmissionPerYearWei` through a governance proposal or to leave it. The value is a base-10 integer in wei.
3. After a change executes, verify that later claims no longer emit the event.
4. Record the incident, including the amounts at the cap.

## Troubleshooting checklist

* If cap-hit events appear earlier than expected, check that the parameter value matches the approved budget (an integer in wei, no stray whitespace).
* If you expected the cap to reset, compare the previous and current year keys.
* For persistent discrepancies, collect the event stream and a state snapshot and escalate to the protocol team.

## Safe parameter changes

The staking parameters in the default governance allow-list (`defaultAllowedGovernanceParams` in `config/config.go`, used when `Governance.AllowedParams` is empty; the sample `config.toml` lists the same eight) are `staking.minimumValidatorStake`, `staking.aprBps`, `staking.payoutPeriodDays`, `staking.unbondingDays`, `staking.minStakeWei`, `staking.maxEmissionPerYearWei`, `staking.rewardAsset` and `staking.compoundDefault`.

The node reads all of them from the governance parameter store (`core/node.go`, and `core/state_transition.go` for the unbonding period, payout period and emission cap). `staking.minStakeWei` is defined but has no enforcement path in the delegation handler (`native/governance/types.go`, comment on `ParamKeyStakingMinStakeWei`). `staking.minimumValidatorStake` is the validator-eligibility threshold, 10,000 ZNHB by default; a stored value that is not a positive base-10 integer degrades to that default and is logged (`MinimumValidatorStakeFromParam`, `native/governance/types.go`).

* Get treasury and governance sign-off before proposing any `staking.*` change, and document the reason and expected effect.
* Try a change on a staging network first and watch the reward index and payouts.
* Prepare a follow-up proposal that restores the previous value in case of unexpected behaviour.
