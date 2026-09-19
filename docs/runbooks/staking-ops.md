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
* The node does not mint the reward. It debits the configured POTSO reward treasury account and credits the claimer, and fails the claim with `staking rewards: treasury not configured` or an insufficient-treasury error when the treasury cannot pay.

## Pause behaviour

* While the `staking` module is paused, delegate, undelegate, unbond-claim and reward-claim transactions fail with `staking: module paused` and each emits a `stake.paused` event with the account, the operation (`delegate`, `undelegate`, `claim`, `claimRewards`), the reason (`paused by governance`) and, for unbond claims, the `unbondingId`.
* The read methods `stake_previewClaim` and `stake_getPosition` answer HTTP `503`, code `-32050` and `staking module paused` while the pause is active.
* Pauses come from the node configuration (`Global.Pauses.Staking`) or from the on-chain pause set. If the configuration says unpaused but the on-chain value is paused, `consensusd` logs a warning and enforces the on-chain value.

## Staking events

1. Confirm the pause from the `stake.paused` events and the governance action that set it.
2. Tell delegators which operations are affected and the expected timeline.
3. Stop automation that retries failed staking transactions.
4. Track pending unbonds approaching maturity so you can tell the affected delegators when the pause lifts.

Optional attributes appear only when they have a value.

1. Inspect the current `system/pauses` map with `go run ./examples/docs/ops/read_pauses`. The entry must read `staking = false` for the module to accept staking transactions.
2. To pause or resume, stage a `gov.v1` `MsgSetPauses` transaction with `pauses.staking` set to `true` or `false`. `examples/docs/ops/pause_toggle` stages such a transaction, but its `--module` flag lists only `lending`, `swap`, `escrow`, `trade`, `loyalty`, `potso`, `transfer_nhb` and `transfer_znhb`, not `staking`.
3. After the transaction executes, confirm both `system/pauses` and the behaviour: staking transactions should succeed, or fail with `staking: module paused`, as intended.

The staking parameters in the default governance allow-list (`config/config.go`) are
`staking.minimumValidatorStake`, `staking.aprBps`, `staking.payoutPeriodDays`,
`staking.unbondingDays`, `staking.minStakeWei`, `staking.maxEmissionPerYearWei`,
`staking.rewardAsset` and `staking.compoundDefault`.

1. Read the year-to-date total and look at recent `stake.emissionCapHit` events.
2. Decide whether to raise `staking.maxEmissionPerYearWei` through a governance proposal or to leave it. The value is a base-10 integer in wei.
3. After a change executes, verify that later claims no longer emit the event.
4. Record the incident, including the amounts at the cap.

## Troubleshooting checklist

* If cap-hit events appear earlier than expected, check that the parameter value matches the approved budget (an integer in wei, no stray whitespace).
* If you expected the cap to reset, compare the previous and current year keys.
* For persistent discrepancies, collect the event stream and a state snapshot and escalate to the protocol team.

## Safe parameter changes

The staking parameters that code reads from the governance parameter store are `staking.aprBps`, `staking.payoutPeriodDays`, `staking.unbondingDays`, `staking.minimumValidatorStake`, `staking.maxEmissionPerYearWei`, and (defined but not enforced for delegation) `staking.minStakeWei` (`native/governance/types.go`).

* Get treasury and governance sign-off before proposing any `staking.*` change, and document the reason and expected effect.
* Try a change on a staging network first and watch the reward index and payouts.
* Prepare a follow-up proposal that restores the previous value in case of unexpected behaviour.
