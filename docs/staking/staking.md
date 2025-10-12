# ZapNHB Staking and Delegation

This document describes the ZapNHB staking pipeline, on-chain state layout, JSON-RPC surface, and emitted events following the introduction of delegation, unbonding, and claim flows. It is intended for auditors, investors, developers, end users, and regulators who require a comprehensive view of the staking module.

> ✅ **General availability and pause control:** Staking is GA on supported networks. Governance manages availability via the `staking.pause.enabled` toggle described in the [governance parameter catalog](../governance/params.md#staking-pause-enabled). When the corresponding `system/pauses` entry reports `staking = false`, delegation, undelegation, and claims are accepted. Operators can unpause the module by executing `gov.v1/MsgSetPauses` with `pauses.staking = false` and can temporarily halt flows by flipping the same flag back to `true`.

## Overview

Stakeholders can lock ZapNHB (ZNHB) balances, optionally delegate voting power to validators, and later queue withdrawals through an unbonding period before claiming their tokens. Rewards accrue at a **fixed 12.5% annual percentage rate (APR)** that is **non-compounding**—each reward period mints a linear share of the annual budget using a global reward index instead of rolling previous interest into the next cycle. Payouts settle on a **30-day interval** (2,592,000 seconds) aligned to the first reward minted after activation. The staking module supports:

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

When a caller has never delegated the staking fields remain zeroed, but the schema is always present so integrators can rely on a consistent payload. If governance pauses staking, read-only calls such as `nhb_getBalance` continue to succeed while mutations return `codeModulePaused`.

### `stake_delegate`

Delegates ZNHB and optionally selects a validator. When governance pauses the module the RPC endpoint returns `codeModulePaused`; otherwise it responds with the caller's updated balances.

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

- **Result**:

```json
{
  "address": "nhb1delegator...",
  "balanceZNHB": "9500",
  "stake": "500",
  "lockedZNHB": "500",
  "delegatedValidator": "nhb1validator...",
  "pendingUnbonds": []
}
```

Companion CLI example:

```bash
nhbctl stake delegate \
  --from nhb1delegator... \
  --amount 1000 \
  --validator nhb1validator...
```

### `stake_undelegate`

Queues an unbonding entry for the caller. Responses include the generated unbond ID and its release timestamp. When staking is paused the RPC responds with `codeModulePaused`.

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
  "id": 12,
  "validator": "nhb1validator...",
  "amount": "500",
  "releaseTime": 1700003600
}
```

CLI example:

```bash
nhbctl stake undelegate \
  --from nhb1delegator... \
  --amount 500
```

### `stake_claim`

Claims a matured unbond entry and returns both the claimed metadata and updated balances. Claims made before the release timestamp are rejected, and paused networks return `codeModulePaused`.

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
    "id": 12,
    "validator": "nhb1validator...",
    "amount": "500",
    "releaseTime": 1700003600
  },
  "balance": {
    "address": "nhb1delegator...",
    "balanceZNHB": "10000",
    "stake": "500",
    "lockedZNHB": "500"
  }
}
```

CLI counterpart:

```bash
nhbctl stake claim \
  --from nhb1delegator... \
  --unbonding-id 2
```

### `stake_previewClaim`

The read-only method `stake_previewClaim` reveals the current monthly payout window for a delegator. Responses include the projected emission for the next period, the reward indexes, and the timestamp when rewards become claimable. The request follows the same authentication rules as `nhb_getBalance`.

```json
{
  "caller": "nhb1delegator..."
}
```

CLI equivalent:

```bash
nhb-cli stake preview nhb1delegator...
```

### Error Semantics

- Calls made while governance has toggled `staking.pause.enabled = true` return `codeModulePaused` with contextual messaging so integrators can surface the blocked state to users.
- Invalid amounts (`<= 0`) trigger `codeInvalidParams`.
- Switching validators without fully removing existing delegation returns a descriptive error.
- Claims before maturity return `unbonding entry is not yet claimable` messages.

## Monitoring & Tooling (Operators)

- **Grafana**: Import [`observability/grafana/staking.json`](../../observability/grafana/staking.json) to visualise daily rewards, bonded supply, pause status, and emission-cap hits. Pair the pause and cap panels with alerts so on-call engineers are paged when staking halts or emissions saturate.
- **CLI quick checks**: Use `nhb-cli` for authenticated spot checks when dashboards are unavailable:

  ```bash
  nhb-cli stake position nhb1delegator...
  ```

  ```
  Stake position for nhb1delegator...
    Shares:       5000000000000000000
    Last index:   285000000000000000000
    Last payout:  2024-06-01T00:00:00Z (1717209600)
  ```

  ```bash
  nhb-cli stake preview nhb1delegator...
  ```

  ```
  Stake rewards preview for nhb1delegator...
    Claimable now: 742.5 ZapNHB
    Next payout:   2024-07-01T00:00:00Z (1719792000)
  ```

- **RPC quick checks**: Issue authenticated JSON-RPC calls directly when automating runbooks or integrating with other systems:

  ```bash
  curl -sS -X POST http://127.0.0.1:8545 \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${RPC_TOKEN}" \
    -d '{
      "jsonrpc": "2.0",
      "id": 1,
      "method": "stake_getPosition",
      "params": ["nhb1delegator..."]
    }'
  ```

  ```json
  {
    "id": 1,
    "jsonrpc": "2.0",
    "result": {
      "shares": "5000000000000000000",
      "lastIndex": "285000000000000000000",
      "lastPayoutTs": 1717209600
    }
  }
  ```

  ```bash
  curl -sS -X POST http://127.0.0.1:8545 \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${RPC_TOKEN}" \
    -d '{
      "jsonrpc": "2.0",
      "id": 2,
      "method": "stake_claimRewards",
      "params": ["nhb1delegator..."]
    }'
  ```

  ```json
  {
    "id": 2,
    "jsonrpc": "2.0",
    "result": {
      "minted": "742500000000000000000",
      "periods": 1,
      "aprBps": 1250,
      "nextEligibleTs": 1719792000
    }
  }
  ```

These commands complement the staking dashboard so operations teams can verify health from shell environments, CI pipelines, or incident response playbooks.

## Event Catalogue (Auditors, Integrators)

| Event | Attributes | Purpose |
| --- | --- | --- |
| `stake.delegated` | `delegator`, `validator`, `amount`, `locked` | Tracks delegation adjustments and validator power changes. |
| `stake.undelegated` | `delegator`, `validator`, `amount`, `releaseTime`, `unbondingId` | Signals the start of an unbonding period. |
| `stake.claimed` | `delegator`, `validator`, `amount`, `unbondingId` | Indicates matured stake reclaimed by the delegator. |
| `stake.rewardsClaimed` | `addr`, `paidZNHB`, `periods`, `aprBps`, `nextEligibleUnix` | Records reward mints when delegators claim accrued payouts. |

These events stream through the existing node event feed so external observers and webhook infrastructure receive timely updates. When governance pauses staking, `stake.paused` events accompany rejected mutations to document the reason.

## Operational Considerations (Regulators & Investors)

- **Unbonding Period**: Fixed at 72 hours to balance liquidity and security. Regulators can audit compliance by inspecting `ReleaseTime` values and event chronology.
- **Validator Accountability**: Validator power is updated immediately upon delegation/undelegation, ensuring consensus weights are accurate.
- **Transparency**: RPC surfaces provide complete insight into locked balances and pending exits, supporting investor reporting and regulatory audits.
- **Safety Checks**: All staking operations are subject to standard authentication, parameter validation, and state consistency checks to prevent unauthorized delegation or premature claims.

## User Experience Notes (End Users)

1. Use `stake_delegate` (or the CLI `stake` command) to lock ZNHB and optionally support a validator. If the network is paused you will receive `codeModulePaused` until governance resumes the module.
2. Monitor `nhb_getBalance` or the CLI `balance` command to view locked stake, delegation target, and pending unbonds. Accounts without historical delegations still return zeroed staking fields.
3. Initiate withdrawals with `stake_undelegate`. Tokens become claimable after ~72 hours; attempts before then are rejected.
4. Complete the process with `stake_claim` to restore ZNHB to the liquid balance once the release time has elapsed.

This flow ensures a predictable staking lifecycle with observable state transitions for all stakeholders.
