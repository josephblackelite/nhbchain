# ZNHB Staking and Delegation

This document describes the ZapNHB staking pipeline, on-chain state layout, JSON-RPC surface, and emitted events following the introduction of delegation, unbonding, and claim flows. It is intended for auditors, investors, developers, end users, and regulators who require a comprehensive view of the staking module.

> **Pause control:** Staking availability is controlled by the `Staking` flag in the node's `[global.Pauses]` config block, which `SetModulePauses` loads into the node (`core/node.go`); the stake RPC handlers return `codeModulePaused` while it is set (`rpc/stake_handlers.go`). The reward engine separately reads the `staking` flag of the `system/pauses` parameter-store entry (`native/params/state/pauses.go`, `core/state/staking_rewards.go`) and returns `ErrStakingPaused` when it is true. There is no `staking.pause.enabled` governance key: it is not in the [governance parameter catalog](../governance/params.md), and no proposal kind writes `system/pauses` (`gov.v1/MsgSetPauses` cannot change chain state, see [governance service](../gov/service.md)).

## Overview

- A holder locks ZNHB (`BalanceZNHB` to `LockedZNHB`) by signing a `TxTypeStake`
  transaction. The lock goes to the holder's own validator address
  (self-stake) or to another address named in the payload (delegation).
- Withdrawing is two transactions: `TxTypeUnstake` starts an unbonding entry,
  and `TxTypeStakeClaim` returns the ZNHB to `BalanceZNHB` once the entry's
  release time has passed.
- Staking rewards are a separate flow: accrual against a global index, claimed
  with `TxTypeStakeClaimRewards` once per payout period.
- The stake of an address also feeds validator eligibility and BFT voting power;
  see the [validator onboarding guide](../validators/onboarding.md).

### Transaction types

Values from `core/types/transaction.go`.

| Type | Value | Signer's payload |
| --- | --- | --- |
| `TxTypeStake` | `0x06` | RLP `[validator bytes, registerValidator bool (optional)]`; `Value` = amount in base units |
| `TxTypeUnstake` | `0x07` | RLP `[validator bytes, deregisterValidator bool (optional)]`; `Value` = amount |
| `TxTypeStakeClaim` | `0x0D` | RLP `[unbondingId uint64]` (required, must be non-zero) |
| `TxTypeStakeClaimRewards` | `0x34` | none |
| `TxTypeSetRewardBeneficiary` | `0x1A` | RLP `[beneficiary string]`; see the onboarding guide |
| `TxTypeHeartbeat` | `0x08` | optional heartbeat payload; see the onboarding guide |

Handlers: `applyStake`, `applyUnstake`, `applyStakeClaim`,
`applyStakeClaimRewards` in `core/state_transition.go` (lines 6507-6626),
dispatched from `handleNativeTransaction` (line 3958 onward). Each of the four
staking transaction types is counted against the `potso` module quota
(`applyQuota(modulePotso, ...)`).

Every one of these operations is refused while the staking module is paused
(see [Pause](#pause)).

### Account fields

`types.Account` (`core/types/account.go`) carries the staking state:

| Field | Meaning |
| --- | --- |
| `Stake` | Voting power attributed to the address as a validator: its own self-stake plus ZNHB delegated in by others. |
| `LockedZNHB` | ZNHB this account has locked, whether self-staked or delegated to someone else. |
| `DelegatedValidator` | 20-byte address the account's `LockedZNHB` is delegated to. Empty when nothing is locked. Equal to the account's own address for self-stake. |
| `PendingUnbonds` | Queue of `StakeUnbond{ID, Validator, Amount, ReleaseTime}`. |
| `NextUnbondingID` | Counter for unbond IDs; the first ID assigned is `1`. |
| `StakeShares`, `StakeLastIndex`, `StakeLastPayoutTs` | Reward accrual state (see [Rewards](#rewards)). |
| `ValidatorRegistered`, `ValidatorRegisteredAt` | Explicit validator opt-in flag and the block time it last turned on. |

### Minimum validator stake

The threshold for validator eligibility is the governance parameter
`staking.minimumValidatorStake`. When it has never been set, the default is
`10000000000000000000000` base units = 10,000 ZNHB
(`native/governance/types.go`, `defaultMinimumValidatorStakeWei`; read in
`core/state_transition.go` `minimumValidatorStake`, line 6997). The
`[global.Staking].MinStakeWei` value in `config.toml` is only validated as an
integer at startup (`core/node.go` `ValidateStakingConfig`); it does not set
this threshold. The parameter `staking.minStakeWei` is in the governance
allow-list but, per its own doc comment (`native/governance/types.go`, line
272), has no enforcement path in the delegation handler.

## Delegation (`TxTypeStake`)

`StakeDelegate` (`core/state_transition.go`, line 5759):

1. Rejected if the staking module is paused (`ErrStakePaused`), if `Value` is
   not positive (`stake must be positive`), or if the validator address is not
   20 bytes.
2. An empty validator field means self-stake (target = the signer).
3. The signer needs `BalanceZNHB >= amount`, else `insufficient ZapNHB`.
4. If the signer already has locked stake delegated to a different address,
   the call fails with `existing delegation must be fully undelegated before
   switching validators`.
5. Effects on the signer: `BalanceZNHB -= amount`, `LockedZNHB += amount`,
   `DelegatedValidator = target`, nonce incremented.
   - Self-stake: `Stake += amount` on the signer.
   - Delegation to another address: `Stake += amount` on the target account,
     the signer is added to the target's delegator index, and the target's
     "delegated-in" total increases by `amount`. There is no check that the
     target is a registered validator.
6. `RegisterValidator = true` in the payload is only accepted with no
   third-party target (`registerValidator is only valid for self-stake`); with
   zero `Value` it is a pure registration that sets the flag without moving
   funds.

## Unbonding (`TxTypeUnstake`)

`StakeUndelegate` (line 5899):

1. Requires `LockedZNHB >= amount` (`insufficient locked stake`) and a non-empty
   `DelegatedValidator` (`no active delegation`).
2. `LockedZNHB -= amount`. For self-stake `Stake -= amount` on the signer; for a
   delegation `Stake -= amount` on the validator account and its delegated-in
   total decreases (`validator stake underflow` if it would go negative).
3. A `StakeUnbond` is appended with `ReleaseTime = block time + unbonding
   period` and the next `NextUnbondingID`.
4. `DelegatedValidator` is cleared when `LockedZNHB` reaches zero.
5. The unbonded amount is not in `BalanceZNHB` yet; it is only claimable after
   `ReleaseTime`.

**Unbonding period.** It is the governance parameter `staking.unbondingDays`
(integer `>= 1`; validator in `native/governance/engine.go`). When that
parameter is unset the fallback is **7 days** (`unbondingPeriod`,
`core/state_transition.go` line 67; `stakingUnbondingPeriod`, line 6459). The
`[global.Staking].UnbondingDays` config value defaults to 7 as well
(`config/config.go`, line 242) but the state transition reads only the
governance parameter and the constant.

A `TxTypeUnstake` with `deregisterValidator = true` and zero `Value` clears the
`ValidatorRegistered` flag without unbonding (`applyUnstake`, line 6555); it is
only valid for a self-stake account (`deregisterValidator is only valid for
self-stake`).

## Claiming unbonded stake (`TxTypeStakeClaim`)

`StakeClaim` (line 6037): `unbondingId` must be non-zero; an unknown ID returns
`unbonding entry N not found`; before `ReleaseTime` it returns `unbonding entry
N is not yet claimable`. On success the entry is removed, `BalanceZNHB +=
amount`, the nonce increments, and a `stake.claimed` event is emitted (see the
note on the duplicate event name below).

## Rewards

Reward code: `core/rewards/engine.go` (index), `core/state_transition.go`
(accrual and claim), `core/state/staking_rewards.go`.

### Global index

`rewards.Engine` keeps a global index that starts at `1e18` and, whenever it is
advanced, grows linearly with elapsed block time:

```
increment = elapsedSeconds * aprBps * 1e18 / (31,536,000 * 10,000)
```

(`core/rewards/engine.go`, `UpdateGlobalIndex`; a year is
`365 * 24 * 60 * 60` seconds.) It is simple interest; nothing compounds. The
engine is advanced by every stake, unstake, claim and APR change
(`advanceStakeRewards`, line 6647).

### APR

The APR in basis points is `[global.Staking].AprBps`, default `1250` (12.5%),
overridable by the governance parameter `staking.aprBps` (`<= 10000`). It is
loaded into the state processor at node startup by `SyncStakingParams`
(`core/node.go` line 1479; call in `cmd/nhb/main.go` line 189).

### Accrual

When an account stakes or unstakes, `accrueStakeAccount` adds
`basis * (index - StakeLastIndex) / 1e18` to `StakeShares` and sets
`StakeLastIndex = index` (`accrueStakeAccountWithBasis`, line 6829). `basis` is
`stakeRewardBasis` (line 6674):

- an account delegating to another validator uses its `LockedZNHB`;
- otherwise it uses `Stake` minus the ZNHB delegated in by others, so a
  validator does not accrue on capital that is not its own.

### Claiming rewards (`TxTypeStakeClaimRewards`)

`StakeClaimRewards` (line 6106), invoked for the signer with no payload:

- The payout period is the governance parameter `staking.payoutPeriodDays`,
  falling back to 30 days (`stakePayoutPeriodDays = 30`, line 72;
  `stakingPayoutPeriodSeconds`, line 6424). A claim is accepted only when at
  least one whole period has elapsed since `StakeLastPayoutTs`; otherwise the
  transaction fails with `stake: claim not yet due` (`ErrNotDue`,
  `core/errors/stake.go`).
- Elapsed whole periods are paid together; the remainder of the index delta is
  kept for later.
- The payout is `StakeShares * eligibleIndexDelta` (line 6310), where
  `eligibleIndexDelta` is the index delta since `StakeLastIndex` scaled by
  whole periods over total elapsed time. The code does not divide by the `1e18`
  index scale here, unlike the accrual step above.
- **The payout is a transfer, not a mint.** It is debited from the treasury
  account configured as `[potso.rewards] TreasuryAddress`
  (`sp.PotsoRewardConfig().TreasuryAddress`) and credited to the claimant. If
  the treasury balance is short the transaction fails with
  `potso.ErrInsufficientTreasury`; if no treasury is configured it fails with
  `staking rewards: treasury not configured`.
- **Annual cap.** The governance parameter `staking.maxEmissionPerYearWei`
  caps the total claimed per UTC calendar year; when it is unset, or `0`, there
  is no cap (`stakingMaxEmissionPerYear`, line 8237). A capped payout emits
  `stake.emissionCapHit`. The `MaxEmissionPerYearWei` value in `config.toml`
  is not read by the state transition.
- After a successful claim `StakeLastPayoutTs` is set to the block time, the
  nonce advances, and the events below are emitted.

The parameters `staking.rewardAsset` and `staking.compoundDefault` are in the
governance allow-list and are copied into the node's runtime configuration
(`core/node.go`, `SyncStakingParams`), but nothing in the state transition
reads them; rewards are always paid in ZNHB and there is no compounding.

## JSON-RPC

The node's JSON-RPC endpoint accepts `POST` with a JSON body. `nhb_getBalance`
needs no authentication; the `stake_*` read methods require a bearer JWT
(`s.requireAuthInto`, `rpc/stake_handlers.go`).

### `nhb_getBalance`

Params: `["<nhb1... address>"]`. Result is `BalanceResponse`
(`rpc/http.go`, line 1069):

```json
{
  "address": "nhb1...",
  "balanceNHB": 0,
  "balanceZNHB": 5000,
  "stake": 1000,
  "lockedZNHB": 1000,
  "delegatedValidator": "nhb1validator...",
  "pendingUnbonds": [
    { "id": 1, "validator": "nhb1validator...", "amount": 1000, "releaseTime": 1700003600 }
  ],
  "unbondingCompletesAt": 1700003600,
  "pendingStakingRewards": 0,
  "username": "example",
  "nonce": 3,
  "engagementScore": 42,
  "validatorRegistered": false
}
```

`delegatedValidator`, `pendingUnbonds`, `unbondingCompletesAt` and
`validatorRegisteredAt` are omitted when empty. Amounts are `big.Int` JSON
numbers. `pendingStakingRewards` is the `payable` value of the claim preview
computed at the node's current time. This method keeps working while staking is
paused.

### `stake_getPosition`

Params: `["<nhb1... address>"]`. Requires auth. Result:

```json
{ "shares": "0", "lastIndex": "0", "lastPayoutTs": 0 }
```

(`rpc/stake_handlers.go`, `stakePositionResult`).

### `stake_previewClaim`

Params: `["<nhb1... address>"]`. Requires auth. Result:

```json
{ "payable": "0", "nextPayoutTs": 0 }
```

`payable` is computed by `Node.StakePreviewClaim` (`core/node.go`, line 5755).

Both read methods return HTTP 503 with code `-32050` (`codeModulePaused`,
message `staking module paused`) while staking is paused, and HTTP 429 with code
`-32020` when the per-source rate limit is exceeded.

### Disabled methods

`stake_delegate`, `stake_undelegate`, `stake_claim` and `stake_claimRewards`
always return HTTP `410 Gone` with code `-32060` (`codeMethodDisabled`) and a
message telling the caller to sign a transaction and submit it with
`nhb_sendTransaction` (`rpc/stake_handlers.go`, `stakeRPCDisabledMessage`). They
were disabled because they trusted a client-supplied address with no signature
proving control of it, or (for `stake_claimRewards`) wrote to state outside
consensus.

To stake, unstake, claim unbonded stake or claim rewards, sign the matching
transaction type and submit it with `nhb_sendTransaction`.

### Errors

- Paused module: `ErrStakePaused` (`staking: module paused`,
  `core/state_transition.go` line 111) from the state transition; HTTP 503 /
  `-32050` from the read methods.
- Non-positive amount: `stake must be positive` / `unstake must be positive`.
- Switching validators with locked stake:
  `existing delegation must be fully undelegated before switching validators`.
- Claim too early: `unbonding entry N is not yet claimable`
  (unbond claim) or `stake: claim not yet due` (reward claim).

## CLI

`nhb-cli` (`cmd/nhb-cli/stake.go`, `main.go`):

| Command | What it does |
| --- | --- |
| `nhb-cli stake <amount> <key_file>` | Signs and sends a `TxTypeStake` with no payload (self-stake). `amount` is a base-10 integer in base units. |
| `nhb-cli un-stake <amount> <key_file>` | Signs and sends a `TxTypeUnstake` (`Value = amount`, no payload). |
| `nhb-cli register-validator <amount> <key_file>` | `TxTypeStake` with `registerValidator = true`. `0` registers without adding stake. |
| `nhb-cli deregister-validator <key_file>` | `TxTypeUnstake` with `deregisterValidator = true` and zero value. |
| `nhb-cli stake position <address>` | Calls `stake_getPosition`; prints shares, last index, last payout. |
| `nhb-cli stake preview <address>` | Calls `stake_previewClaim`; prints `Claimable now` and the next payout time. |
| `nhb-cli stake claim <address>` | Calls `stake_claimRewards`, which the server always rejects with 410 (see Disabled methods), so this command cannot succeed. |
| `nhb-cli balance <address>` | Calls `nhb_getBalance`; prints staked, locked, delegated validator, validator registration, pending unbonds. |

There is no `nhb-cli` command that submits `TxTypeStakeClaim` or
`TxTypeStakeClaimRewards`, or one that delegates to another validator. Every
transaction command needs `NHB_RPC_TOKEN` set, and the CLI defaults to
`http://localhost:8080` unless `RPC_URL` or `--rpc` is set.

## Events

Attribute names below are what the code emits (`core/events/stake.go`,
`core/state_transition.go`).

| Event | Attributes | Emitted by |
| --- | --- | --- |
| `stake.delegated` | `addr`, `sharesAdded`, `newShares`, `lastIndex`, `validator`, `amount`, `locked` (`locked` only on the delegator's event) | `StakeDelegate`; one event for the delegator and, for a third-party target, one for the validator account |
| `stake.undelegated` | `addr`, `sharesRemoved`, `newShares`, `lastIndex`, `validator`, `amount`, `releaseTime`, `unbondingId` (release time and ID only on the delegator's event) | `StakeUndelegate` |
| `stake.claimed` (unbond claim) | `delegator`, `validator`, `amount`, `unbondingId` | `StakeClaim` |
| `stake.rewardsClaimed` | `addr`, `paidZNHB`, `periods`, `aprBps`, `nextEligibleUnix` | `StakeClaimRewards` |
| `stake.claimed` (reward claim alias) | `addr`, `minted`, `periods`, `aprBps`, `nextEligibleUnix` | `StakeClaimRewards`, alongside `stake.rewardsClaimed` |
| `stake.emissionCapHit` | `requestedZNHB`, `attemptedZNHB`, `allowedZNHB`, `ytd`, `cap` | `StakeClaimRewards` when the annual cap limits a payout |
| `stake.paused` | `addr`, `operation` (`delegate`, `undelegate`, `claim`, `claimRewards`), `reason`, `unbondingId` | Appended when a staking call is refused because of the pause; see the note below |
| `stake.validatorRegistrationChanged` | `addr`, `registered`, `at` | `setValidatorRegistered` |

Two different events share the name `stake.claimed`: the unbond-claim event and
the legacy alias of the reward claim. Consumers must tell them apart by their
attributes.

`executeTransaction` drops the events appended by a transaction that returns an
error (`core/state_transition.go`, lines 2851-2869, except the transfer-paused
and sponsorship errors), so the `stake.paused` event is appended but discarded
together with the rejected transaction.

## Pause

`staking` is one of the module pause flags. The state transition reads it from
the on-chain parameter entry `system/pauses` (`native/params/keys.go`), reloaded
by `refreshModulePauses` (`core/node.go`, line 804). Local `config.toml`
`[global.Pauses].Staking` is not enforced against that value: at startup the
node only logs a warning if the two disagree
(`cmd/nhb/main.go` line 165, `Node.StakingPauseOnChain`). This repository
contains no code path that writes `system/pauses`, so how the flag is changed
is not described here.

## Observability

`observability/metrics.go` registers `nhb_staking_rewards_paid_zn`,
`nhb_staking_paused`, `nhb_staking_total_staked{account}`, `nhb_staking_cap_hit`
and `nhb_staking_index_persist_failures_total`. A dashboard definition is in
[`observability/grafana/staking.json`](../../observability/grafana/staking.json).
