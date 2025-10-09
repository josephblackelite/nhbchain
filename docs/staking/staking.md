# ZapNHB Staking and Delegation

This document describes the ZapNHB staking pipeline, on-chain state layout, JSON-RPC surface, and emitted events following the introduction of delegation, unbonding, and claim flows. It is intended for auditors, investors, developers, end users, and regulators who require a comprehensive view of the staking module.

> ⚠️ **Preview / Disabled:** ZapNHB staking has not yet been activated on any public network. All RPC workflows documented below will return `codeFeatureDisabled` until governance ratifies the staking program and executes the enabling proposal. Monitor the [Governance Lifecycle overview](../governance/overview.md#proposal-intake) for the ratification proposal and activation announcement.

## Overview

Once governance activates the program, stakeholders will be able to lock ZapNHB (ZNHB) balances, optionally delegate voting power to validators, and later queue withdrawals through an unbonding period before claiming their tokens. Rewards accrue at a **fixed 12.5% annual percentage rate (APR)** that is **non-compounding**—each reward period mints a linear share of the annual budget using a global reward index instead of rolling previous interest into the next cycle. Payouts settle on a **30-day interval** (2,592,000 seconds) aligned to the first reward minted after activation. The staking module supports:

- Self-staking and third-party delegation with validator power tracking.
- Deterministic unbonding queues per delegator with a 72-hour release window.
- A global reward index model that updates proportionally to stake and time, avoiding compounding interest.
- JSON-RPC endpoints and CLI commands to delegate, undelegate, claim matured stake, and inspect reward indexes.
- Event emission for delegated, undelegated, claimed stake, reward index updates, and emission-cap events.
- Expanded balance queries that expose locked amounts, delegation targets, reward indexes, pending unbond queues, and unclaimed rewards.

### Reward Index Model (Auditors & Developers)

Rewards accrue through a pair of monotonically increasing indexes: a global reward index that advances every 30-day interval and per-delegator cursors stored in account metadata. When the network mints rewards, it increments the global index by `(targetAPR / 12) * indexScale` to reflect the monthly share of the 12.5% APR. Individual delegators earn `lockedZNHB * (globalIndex - delegatorIndex)` and then advance their personal index cursor. Because the calculation references the difference between indexes, rewards do **not** compound—the principal stays constant across periods while unclaimed rewards accumulate separately.

Delegators can safely miss one or more 30-day payouts: their index cursor advances when they eventually claim, minting the entire accrued balance in a single transaction.

## State Model (Auditors & Developers)

Each account (`types.Account`) now contains the following staking-related fields:

| Field | Type | Description |
| --- | --- | --- |
| `Stake` | `*big.Int` | Amount of voting power currently attributed to the account as a validator. Includes self-stake and delegated stake. |
| `LockedZNHB` | `*big.Int` | Total ZapNHB locked by the account and actively delegated. |
| `DelegatedValidator` | `[]byte` | Raw 20-byte address of the validator receiving this account's delegation. Empty when not delegated. |
| `PendingUnbonds` | `[]types.StakeUnbond` | Queue of unbonding entries awaiting maturity. Each entry carries the unbond ID, validator, amount, and UNIX release time. |
| `NextUnbondingID` | `uint64` | Monotonic counter used to assign unique IDs to new unbond entries. |

Metadata is persisted via the account metadata trie and automatically populated for existing accounts with zero values. Validator set updates occur whenever an account's `Stake` meets or exceeds the configurable `staking.minimumValidatorStake` parameter. Networks that have not yet set this governance parameter fall back to the legacy 1,000 ZNHB threshold exposed by `DefaultMinimumValidatorStake`. Operators can inspect or propose adjustments to this threshold through the [governance parameter catalog](../governance/params.md).

### Unbonding Entries

`types.StakeUnbond` captures pending releases:

- `ID`: Unique sequence per delegator.
- `Validator`: 20-byte address that previously held the delegation.
- `Amount`: ZNHB scheduled for release.
- `ReleaseTime`: UNIX timestamp when the delegator can claim.

Entries are appended when `StakeUndelegate` is invoked and removed upon successful `StakeClaim` calls after the release time has elapsed.

## Business Logic (Auditors, Developers, Regulators)

### Delegation Flow

1. **Eligibility**: Delegator must hold sufficient liquid ZNHB and optionally specify a validator address. Omitted validator defaults to self.
2. **State mutations**:
   - Deduct ZNHB from `BalanceZNHB` and increment `LockedZNHB`.
   - Record validator in `DelegatedValidator` (unless delegation cleared).
   - Increase validator `Stake` to reflect new voting power.
   - Append `stake.delegated` event with amount and validator metadata.

3. **Validator Accounting**: Self-delegation increases both `LockedZNHB` and `Stake`. Delegation to another validator only touches `LockedZNHB` for the delegator while increasing the validator's `Stake`.

### Unbonding Flow

1. **Preconditions**: Delegator must have sufficient locked ZNHB and an active delegation.
2. **Execution**:
   - Decrease `LockedZNHB` by the requested amount.
   - If self-staked, reduce `Stake` proportionally.
   - Generate a new unbond entry with `ReleaseTime = now + 72h`.
   - Clear `DelegatedValidator` when no locked stake remains.
   - Decrease validator `Stake` when delegating away from another validator.
   - Emit `stake.undelegated` event (contains amount, validator, release time, unbond ID).

3. **Claiming**: Before release time, claims are rejected. After maturity, tokens are returned to `BalanceZNHB`, the unbond entry is removed, and a `stake.claimed` event is emitted.

## JSON-RPC Interface (Developers & Integrators)

### Updated Balance Query

`nhb_getBalance` now returns the extended `BalanceResponse` payload, including the delegator-specific reward index cursor and unclaimed reward amount:

```json
{
  "address": "nhb1...",
  "balanceNHB": "0",
  "balanceZNHB": "5000",
  "stake": "1000",
  "lockedZNHB": "1000",
  "delegatedValidator": "nhb1validator...",
  "rewardIndex": "285000000000000000000", // scaled global index snapshot
  "delegatorIndex": "280000000000000000000", // delegator's personal cursor
  "accruedRewards": "520", // total rewards yet to claim
 "pendingUnbonds": [
    {
      "id": 1,
      "validator": "nhb1validator...",
      "amount": "1000",
      "releaseTime": 1700003600
    }
  ],
  "username": "example",
  "nonce": 3,
  "engagementScore": 42
}
```

While staking is disabled, balances for accounts without historical delegations continue to show zeroed staking fields and no pending unbonds. The RPC surface preserves the schema so integrators can build against the final shape without conditional parsing.

### `stake_delegate`

Delegates ZNHB and optionally selects a validator. While staking remains disabled, the RPC endpoint immediately rejects submissions with `codeFeatureDisabled`.

- **Method**: `stake_delegate`
- **Auth**: Required (same token as `nhb_sendTransaction`).
- **Params**:

```json
{
  "caller": "nhb1delegator...",
  "amount": "1000",
  "validator": "nhb1validator..." // optional
}
```

- **Result (disabled preview)**:

```json
{
  "error": {
    "code": "codeFeatureDisabled",
    "message": "staking delegation is unavailable until governance activates the module"
  }
}
```

Companion CLI example (available once staking launches):

```bash
nhbctl stake delegate \
  --from nhb1delegator... \
  --amount 1000 \
  --validator nhb1validator...
```

### `stake_undelegate`

Queues an unbonding entry for the caller. During the preview phase the method returns the same disabled error code shown above.

- **Method**: `stake_undelegate`
- **Params**:

```json
{
  "caller": "nhb1delegator...",
  "amount": "500"
}
```

- **Result (disabled preview)**: Identical to the delegation error payload until the module is enabled.

CLI preview (returns an error until activation):

```bash
nhbctl stake undelegate \
  --from nhb1delegator... \
  --amount 500
```

### `stake_claim`

Claims a matured unbond entry and returns both the claimed metadata and updated balances. As with the other endpoints, the disabled preview currently rejects requests with `codeFeatureDisabled`.

- **Method**: `stake_claim`
- **Params**:

```json
{
  "caller": "nhb1delegator...",
  "unbondingId": 2
}
```

- **Result (disabled preview)**: Identical to the delegation error payload until activation.

CLI counterpart:

```bash
nhbctl stake claim \
  --from nhb1delegator... \
  --unbonding-id 2
```

### `stake_getRewardPreview`

The read-only method `stake_getRewardPreview` reveals the current monthly payout window for a delegator. Responses include the global reward index, the delegator index, the next payout timestamp (every 30 days from activation), and the projected emission based on the non-compounding model. The request follows the same authentication rules as `nhb_getBalance`.

```json
{
  "caller": "nhb1delegator..."
}
```

CLI equivalent:

```bash
nhbctl stake rewards --from nhb1delegator...
```

### Error Semantics

- Calls made before activation return `codeFeatureDisabled` with contextual messaging so integrators can surface the blocked state to users.
- Invalid amounts (`<= 0`) trigger `codeInvalidParams`.
- Switching validators without fully removing existing delegation returns a descriptive error.
- Claims before maturity return `unbonding entry is not yet claimable` messages.

## Event Catalogue (Auditors, Integrators)

| Event | Attributes | Purpose |
| --- | --- | --- |
| `stake.delegated` | `delegator`, `validator`, `amount`, `locked` | Tracks delegation adjustments and validator power changes. |
| `stake.undelegated` | `delegator`, `validator`, `amount`, `releaseTime`, `unbondingId` | Signals the start of an unbonding period. |
| `stake.claimed` | `delegator`, `validator`, `amount`, `unbondingId` | Indicates matured stake reclaimed by the delegator. |

These events are added to the existing node event stream so external observers and webhook infrastructure receive timely updates. They remain dormant until governance activates the staking module.

## Operational Considerations (Regulators & Investors)

- **Unbonding Period**: Fixed at 72 hours to balance liquidity and security. Regulators can audit compliance by inspecting `ReleaseTime` values and event chronology.
- **Validator Accountability**: Validator power is updated immediately upon delegation/undelegation, ensuring consensus weights are accurate.
- **Transparency**: RPC surfaces provide complete insight into locked balances and pending exits, supporting investor reporting and regulatory audits.
- **Safety Checks**: All staking operations are subject to standard authentication, parameter validation, and state consistency checks to prevent unauthorized delegation or premature claims.

## User Experience Notes (End Users)

1. Use `stake_delegate` (or the CLI `stake` command) to lock ZNHB and optionally support a validator once enabled. During the preview period the command returns `codeFeatureDisabled`.
2. Monitor `nhb_getBalance` or the CLI `balance` command to view locked stake, delegation target, and pending unbonds after launch; until activation the response omits staking data for accounts without historical delegations.
3. Initiate withdrawals with `stake_undelegate`. Tokens become claimable after ~72 hours. In the disabled state, the CLI mirrors the RPC error payload described above.
4. Complete the process with `stake_claim` to restore ZNHB to the liquid balance when claims are supported. Prior to activation the command returns `codeFeatureDisabled`.

This flow ensures a predictable staking lifecycle with observable state transitions for all stakeholders.
