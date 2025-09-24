# ZapNHB Staking and Delegation

This document describes the ZapNHB staking pipeline, on-chain state layout, JSON-RPC surface, and emitted events following the introduction of delegation, unbonding, and claim flows. It is intended for auditors, investors, developers, end users, and regulators who require a comprehensive view of the staking module.

## Overview

Stakeholders can now lock ZapNHB (ZNHB) balances, optionally delegate voting power to validators, and later queue withdrawals through an unbonding period before claiming their tokens. The staking module supports:

- Self-staking and third-party delegation with validator power tracking.
- Deterministic unbonding queues per delegator with a 72-hour release window.
- JSON-RPC endpoints to delegate, undelegate, and claim matured stake.
- Event emission for delegated, undelegated, and claimed stake transitions.
- Expanded balance queries that expose locked amounts, delegation targets, and unbond queues.

## State Model (Auditors & Developers)

Each account (`types.Account`) now contains the following staking-related fields:

| Field | Type | Description |
| --- | --- | --- |
| `Stake` | `*big.Int` | Amount of voting power currently attributed to the account as a validator. Includes self-stake and delegated stake. |
| `LockedZNHB` | `*big.Int` | Total ZapNHB locked by the account and actively delegated. |
| `DelegatedValidator` | `[]byte` | Raw 20-byte address of the validator receiving this account's delegation. Empty when not delegated. |
| `PendingUnbonds` | `[]types.StakeUnbond` | Queue of unbonding entries awaiting maturity. Each entry carries the unbond ID, validator, amount, and UNIX release time. |
| `NextUnbondingID` | `uint64` | Monotonic counter used to assign unique IDs to new unbond entries. |

Metadata is persisted via the account metadata trie and automatically populated for existing accounts with zero values. Validator set updates occur whenever an account's `Stake` crosses the minimum threshold (1,000 ZNHB by default).

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

`nhb_getBalance` now returns the extended `BalanceResponse` payload:

```json
{
  "address": "nhb1...",
  "balanceNHB": "0",
  "balanceZNHB": "5000",
  "stake": "1000",
  "lockedZNHB": "1000",
  "delegatedValidator": "nhb1validator...",
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

### `stake_delegate`

Delegates ZNHB and optionally selects a validator.

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

- **Result**: Updated balance snapshot identical to `nhb_getBalance`.

### `stake_undelegate`

Queues an unbonding entry for the caller.

- **Method**: `stake_undelegate`
- **Params**:

```json
{
  "caller": "nhb1delegator...",
  "amount": "500"
}
```

- **Result**:

```json
{
  "id": 2,
  "validator": "nhb1validator...",
  "amount": "500",
  "releaseTime": 1700003600
}
```

### `stake_claim`

Claims a matured unbond entry and returns both the claimed metadata and updated balances.

- **Method**: `stake_claim`
- **Params**:

```json
{
  "caller": "nhb1delegator...",
  "unbondingId": 2
}
```

- **Result**:

```json
{
  "claimed": {
    "id": 2,
    "validator": "nhb1validator...",
    "amount": "500",
    "releaseTime": 1700003600
  },
  "balance": {
    "address": "nhb1delegator...",
    "balanceZNHB": "4500",
    "lockedZNHB": "500",
    "pendingUnbonds": []
  }
}
```

### Error Semantics

- Invalid amounts (`<= 0`) trigger `codeInvalidParams`.
- Switching validators without fully removing existing delegation returns a descriptive error.
- Claims before maturity return `unbonding entry is not yet claimable` messages.

## Event Catalogue (Auditors, Integrators)

| Event | Attributes | Purpose |
| --- | --- | --- |
| `stake.delegated` | `delegator`, `validator`, `amount`, `locked` | Tracks delegation adjustments and validator power changes. |
| `stake.undelegated` | `delegator`, `validator`, `amount`, `releaseTime`, `unbondingId` | Signals the start of an unbonding period. |
| `stake.claimed` | `delegator`, `validator`, `amount`, `unbondingId` | Indicates matured stake reclaimed by the delegator. |

These events are added to the existing node event stream so external observers and webhook infrastructure receive timely updates.

## Operational Considerations (Regulators & Investors)

- **Unbonding Period**: Fixed at 72 hours to balance liquidity and security. Regulators can audit compliance by inspecting `ReleaseTime` values and event chronology.
- **Validator Accountability**: Validator power is updated immediately upon delegation/undelegation, ensuring consensus weights are accurate.
- **Transparency**: RPC surfaces provide complete insight into locked balances and pending exits, supporting investor reporting and regulatory audits.
- **Safety Checks**: All staking operations are subject to standard authentication, parameter validation, and state consistency checks to prevent unauthorized delegation or premature claims.

## User Experience Notes (End Users)

1. Use `stake_delegate` (or the CLI `stake` command) to lock ZNHB and optionally support a validator.
2. Monitor `nhb_getBalance` or the CLI `balance` command to view locked stake, delegation target, and pending unbonds.
3. Initiate withdrawals with `stake_undelegate`. Tokens become claimable after ~72 hours.
4. Complete the process with `stake_claim` to restore ZNHB to the liquid balance.

This flow ensures a predictable staking lifecycle with observable state transitions for all stakeholders.
