# Staking Emission Operations

This runbook covers the annual emission cap on staking rewards, the staking pause flag, and
the events and metrics an operator can watch. All statements come from
`core/state_transition.go`, `core/state`, `core/events/stake.go`, `rpc/stake_handlers.go` and
`native/governance`.

## Transactions and RPC

Staking state changes are signed transactions: `TxTypeStake` (0x06), `TxTypeUnstake` (0x07),
`TxTypeStakeClaim` (0x0D, claims a matured unbond by `unbondingId`) and
`TxTypeStakeClaimRewards` (0x34, claims accrued rewards for the signer). The JSON-RPC methods
`stake_delegate`, `stake_undelegate`, `stake_claim` and `stake_claimRewards` are disabled and
return HTTP 410 with code `-32060`. The read methods `stake_getPosition` (returns `shares`,
`lastIndex`, `lastPayoutTs`) and `stake_previewClaim` (returns `payable`, `nextPayoutTs`) take
one bech32 address parameter and require RPC authentication. All four staking transaction
types count against the `potso` module quota (see
[Pause and quota operations](./pause-and-quotas.md)).

A reward claim fails with `stake: claim not yet due` until the payout period has elapsed
since the account's last payout. The period is `staking.payoutPeriodDays` from the parameter
store, default 30 days.

## Monitor year-to-date emissions

* The amount paid out in a UTC calendar year is stored under the state key
  `staking/ytdEmissions/<YYYY>` (for example `staking/ytdEmissions/2026`), as a base-10
  integer in wei. The year comes from the block timestamp of the claim. A new year uses a new
  key; nothing needs resetting.
* Staking rewards are paid from the POTSO reward treasury: the claim debits the treasury
  account set in the POTSO reward configuration and credits the claimant, and it adds the
  amount to the year-to-date counter. The claim fails with `staking rewards: treasury not
  configured` when no treasury is set, and with the POTSO insufficient-treasury error when
  the treasury balance is lower than the payout.
* There is no CLI command for the counter. Read the key with a tool that opens the state
  trie (`Manager.StakingEmissionYTD` in `core/state/manager.go`).
* Metrics registered by the node (`observability/metrics.go`): `nhb_staking_rewards_paid_zn`
  (counter), `nhb_staking_cap_hit` (counter), `nhb_staking_paused` (gauge, 1 when paused),
  `nhb_staking_total_staked{account}` (gauge) and `nhb_staking_index_persist_failures_total`
  (counter of staking reward index persistence failures).

## The emission cap

* **Where the cap comes from.** Claims read `staking.maxEmissionPerYearWei` from the
  governance parameter store only. When the parameter has never been set, or is empty or
  `0`, there is no cap and no cap check runs. The `MaxEmissionPerYearWei` value in
  `[global.Staking]` of `config.toml` (default `5000000000000000000`) is not what a claim
  checks. The parameter is in the default governance allow-list (`config/config.go`), so it
  is changed with a `param.update` proposal. Write the value as an unquoted JSON integer in
  wei: the proposal validator also accepts a quoted decimal string, but the stored text is
  parsed verbatim as a base-10 integer when a claim runs (`stakingMaxEmissionPerYear` in
  `core/state_transition.go`), so a quoted value would make claims fail with
  `invalid max emission value`.
* **When a claim would exceed the cap.** The payout is cut down to what fits under the cap
  (rounded down to a whole reward-index step per share), and the node appends a
  `stake.emissionCapHit` event. When no headroom is left the claim succeeds with a payout of
  `0` and the event is still emitted. The unpaid part stays claimable later because the
  account's reward index only advances by the amount actually applied.
* **Event attributes.** `stake.emissionCapHit` carries `requestedZNHB`, `attemptedZNHB` (same
  value), `allowedZNHB`, `ytd` and `cap`, all in wei.
* **Responding.** Confirm the counter and the recent cap events. If governance raises the
  parameter, later claims stop emitting the event. Record the amount paid at the cap.

## Pause behaviour

* The `Staking` flag of the module pause map (see
  [Pause and quota operations](./pause-and-quotas.md) for how the map is stored and for what
  is and is not possible today) makes the state processor reject delegation, undelegation,
  unbond claims and reward claims with the error `staking: module paused`. Each rejection
  appends a `stake.paused` event with `addr`, `operation` (`delegate`, `undelegate`, `claim`
  or `claimRewards`), `reason` (`paused by governance`) and, for unbond claims, `unbondingId`.
* While the flag is set, `stake_getPosition` and `stake_previewClaim` return HTTP 503 with
  JSON-RPC code `-32050` and the message `staking module paused`.
* The code has no separate "pause enabled" parameter and no percentage-of-cap pause alerts.

## Staking events

| Event | Attributes |
| --- | --- |
| `stake.delegated` | `addr`, `sharesAdded`, `newShares`, `lastIndex`, `validator`, `amount`, `locked` |
| `stake.undelegated` | `addr`, `sharesRemoved`, `newShares`, `lastIndex`, `validator`, `amount`, `releaseTime`, `unbondingId` |
| `stake.rewardsClaimed` | `addr`, `paidZNHB`, `periods`, `aprBps`, `nextEligibleUnix` |
| `stake.claimed` | Two shapes share this type. A reward claim emits it as a legacy alias of `stake.rewardsClaimed` with `addr`, `minted`, `periods`, `aprBps`, `nextEligibleUnix`. An unbond claim emits `delegator`, `validator`, `amount`, `unbondingId`. |
| `stake.emissionCapHit` | see above |
| `stake.paused` | see above |
| `stake.validatorRegistrationChanged` | `addr`, `registered`, `at` |

Optional attributes appear only when they have a value.

## Governance parameters

The staking parameters in the default governance allow-list (`config/config.go`) are
`staking.minimumValidatorStake`, `staking.aprBps`, `staking.payoutPeriodDays`,
`staking.unbondingDays`, `staking.minStakeWei`, `staking.maxEmissionPerYearWei`,
`staking.rewardAsset` and `staking.compoundDefault`.

* `staking.minimumValidatorStake` is the validator eligibility floor. Without a governance
  value it defaults to `10000000000000000000000` wei, 10,000 ZNHB
  (`DefaultMinimumValidatorStake` in `native/governance/types.go`).
* `staking.minStakeWei` is documented in the code as having no enforcement path in the
  delegation handler (`native/governance/types.go`).
* Keep a follow-up proposal ready that restores the previous value before changing any of
  them.
