# ZNHB Staking and Delegation

This page describes what the staking code does: the transaction types that change
staking state, the JSON-RPC methods that read it, the parameters that shape it,
and the events it emits. Everything here is taken from the code; where a
statement depends on a specific function, the function is named. For the CLI
commands see [`docs/cli/stake.md`](../cli/stake.md) and
[`docs/cli/staking.md`](../cli/staking.md).

## How staking state changes

Staking state changes only through signed transactions submitted with
`nhb_sendTransaction`. The old JSON-RPC methods that changed staking state are
disabled (see [JSON-RPC methods](#json-rpc-methods)).

| Action | Transaction type | Payload (RLP-encoded in `data`) | Handler |
| --- | --- | --- | --- |
| Stake / delegate | `TxTypeStake` (`0x06`) | Optional `{Validator []byte, RegisterValidator bool}`. No payload stakes to the signer's own address. `Value` is the amount. | `applyStake` -> `StakeDelegate` |
| Unstake (start unbonding) | `TxTypeUnstake` (`0x07`) | Optional `{Validator []byte, DeregisterValidator bool}`. `Value` is the amount. | `applyUnstake` -> `StakeUndelegate` |
| Claim a matured unbond | `TxTypeStakeClaim` (`0x0D`) | Required `{UnbondingID uint64}`, greater than zero. | `applyStakeClaim` -> `StakeClaim` |
| Claim staking rewards | `TxTypeStakeClaimRewards` (`0x34`) | None. | `applyStakeClaimRewards` -> `StakeClaimRewards` |

The handlers are in `core/state_transition.go`; the type constants are in
`core/types/transaction.go`.

### Delegation (`StakeDelegate`)

- The amount must be positive (`stake must be positive`) and the delegator must
  hold enough liquid ZNHB (`insufficient ZapNHB`).
- An empty validator means the delegator itself; otherwise the validator address
  must be 20 bytes.
- A delegator that already has locked stake with one validator cannot delegate to
  a different one until it has fully undelegated (`existing delegation must be
  fully undelegated before switching validators`).
- The amount moves from `BalanceZNHB` to `LockedZNHB`, `DelegatedValidator` is
  set, and the target validator's `Stake` grows by the amount. For a
  self-stake the delegator's own `Stake` grows.
- Whether an account counts as a validator (registered, self-delegated, meets the
  minimum stake) is described in [`docs/cli/staking.md`](../cli/staking.md).

### Unbonding (`StakeUndelegate`)

- The amount must be positive (`unstake must be positive`), no more than the
  account's `LockedZNHB` (`insufficient locked stake`), and the account must have
  an active delegation (`no active delegation`).
- `LockedZNHB` decreases by the amount and a `types.StakeUnbond` entry
  (`ID`, `Validator`, `Amount`, `ReleaseTime`) is appended to `PendingUnbonds`.
  IDs come from `NextUnbondingID`, starting at 1.
- `ReleaseTime` is the block timestamp plus the unbonding period. The period is
  the governance parameter `staking.unbondingDays`; when it is unset it is 7 days
  (`unbondingPeriod`, `stakingUnbondingPeriod`).
- When no locked stake remains, `DelegatedValidator` is cleared.

### Claiming an unbond (`StakeClaim`)

- The entry must exist (`unbonding entry N not found`) and its release time must
  have passed (`unbonding entry N is not yet claimable`). The amount then returns
  to `BalanceZNHB` and the entry is removed.

### Claiming rewards (`StakeClaimRewards`)

- Rewards are claimable once per payout period. The period is the governance
  parameter `staking.payoutPeriodDays`, defaulting to 30 days
  (`stakePayoutPeriodDays`). A claim made before a full period has elapsed since
  the last payout fails with `stake: claim not yet due` (`core/errors/stake.go`).
  A claim covers every whole period that has elapsed.
- The global reward index grows linearly with time:
  `delta_seconds * aprBps * 1e18 / (31,536,000 * 10,000)` per update
  (`core/rewards/engine.go`). It is not compounded. The APR is the governance
  parameter `staking.aprBps`.
- Payouts are limited by the annual emission cap `staking.maxEmissionPerYearWei`
  when it is greater than zero; a claim that would exceed it is reduced to the
  remaining headroom and emits `stake.emissionCapHit`.
- The claimed amount is debited from the balance of the POTSO reward treasury
  address (`sp.PotsoRewardConfig().TreasuryAddress`) and credited to the claimer.
  If no treasury is configured the claim fails with `staking rewards: treasury not
  configured`; if the treasury cannot cover it, with `potso.ErrInsufficientTreasury`.

The configuration defaults for these parameters are `AprBps` 1250,
`PayoutPeriodDays` 30, `UnbondingDays` 7 and `MaxEmissionPerYearWei`
`5000000000000000000` (`config/config.go`, `Staking` block). The values the node
uses come from the governance parameter store when a parameter has been set
(`core/node.go`, around lines 1493-1535).

### Pausing

While the `staking` module is paused, `StakeDelegate`, `StakeUndelegate`,
`StakeClaim` and `StakeClaimRewards` fail with `staking: module paused`
(`ErrStakePaused`) and emit a `stake.paused` event. The pause comes from the
node configuration (`Global.Pauses.Staking`) or from the on-chain pause set
(`consensusd` logs a warning when the two disagree; the node enforces the
on-chain value). The [runbook](../runbooks/staking-ops.md) covers operations.

## Account fields

`types.Account` carries these staking fields (`core/types/account.go`):

| Field | Meaning |
| --- | --- |
| `Stake` | Voting power attributed to the account as a validator: its own self-stake plus stake delegated to it. |
| `LockedZNHB` | ZNHB the account has locked in a delegation. |
| `DelegatedValidator` | 20-byte address the account delegates to; empty when it has no delegation. |
| `PendingUnbonds` | Unbonding entries awaiting their release time. |
| `NextUnbondingID` | Counter used to assign unbond IDs. |
| `StakeShares`, `StakeLastIndex`, `StakeLastPayoutTs` | Reward accounting: accrued reward shares, the global index at the account's last accrual, and the timestamp of its last payout. |
| `ValidatorRegistered`, `ValidatorRegisteredAt` | Explicit validator registration flag and the block time it was set. |

## JSON-RPC methods

### Disabled: `stake_delegate`, `stake_undelegate`, `stake_claim`, `stake_claimRewards`

These four methods are permanently disabled. Each handler in
`rpc/stake_handlers.go` answers every call, whatever the parameters, with HTTP
`410 Gone`, JSON-RPC code `-32060` (`codeMethodDisabled`, `rpc/http.go`) and this
message:

```
this method is disabled; sign a transaction (TxTypeStake/TxTypeUnstake/TxTypeStakeClaim/TxTypeStakeClaimRewards) via nhb_sendTransaction instead, so the caller's own signature authorizes the action
```

They were disabled because they accepted a caller address without a signature, or
changed state outside block execution (comment above `stakeRPCDisabledMessage`).
Use the transactions in the table above instead. In particular there is no
working `stake_claimRewards` result with `minted`, `periods` or `nextEligibleTs`
fields: reward claims are `TxTypeStakeClaimRewards` transactions, and a claim
that is not yet due fails in the state transition with `stake: claim not yet due`.

### Read methods

Both methods below require the bearer token (`requireAuthInto`), take one
parameter, a bech32 address, and are subject to the per-source rate limit
(`staking rate limit exceeded`, HTTP `429`, code `-32020`). While the staking
module is paused they answer HTTP `503`, code `-32050`, `staking module paused`
(`guardStakeRequest`).

`stake_getPosition` returns the account's reward accounting fields:

```json
{ "id": 4, "jsonrpc": "2.0", "method": "stake_getPosition", "params": ["nhb1..."] }
```

```json
{
  "id": 4,
  "jsonrpc": "2.0",
  "result": { "shares": "5000000000000000000", "lastIndex": "1500", "lastPayoutTs": 1717387200 }
}
```

`stake_previewClaim` returns what `Node.StakePreviewClaim` computes for the
current time: `payable` (a decimal string, `"0"` when no full payout period has
elapsed) and `nextPayoutTs` (Unix seconds).

```json
{ "id": 3, "jsonrpc": "2.0", "method": "stake_previewClaim", "params": ["nhb1..."] }
```

```json
{ "id": 3, "jsonrpc": "2.0", "result": { "payable": "7425000000000000000000", "nextPayoutTs": 1719969600 } }
```

CLI equivalents: `nhb-cli stake position <address>` and
`nhb-cli stake preview <address>`.

### `nhb_getBalance`

The public `nhb_getBalance` method returns `BalanceResponse` (`rpc/http.go`):
`address`, `balanceNHB`, `balanceZNHB`, `stake`, `lockedZNHB`, and when set
`delegatedValidator`, `pendingUnbonds` (each with `id`, `validator`, `amount`,
`releaseTime`), `unbondingCompletesAt` and `pendingStakingRewards`, plus
`username`, `nonce`, `engagementScore`, `validatorRegistered` and, when set,
`validatorRegisteredAt`. It does not return a reward index or delegator index
field. It keeps working while staking is paused.

## Events

Attribute names are those set in `core/events/stake.go` and
`core/state_transition.go`.

| Event | Attributes | Emitted when |
| --- | --- | --- |
| `stake.delegated` | `addr`, `sharesAdded`, `newShares`, `lastIndex`, `validator`, `amount`, `locked` | A delegation. One event for the delegator (with `locked`) and, when delegating to another validator, one for the validator (without `locked`). |
| `stake.undelegated` | `addr`, `sharesRemoved`, `newShares`, `lastIndex`, `validator`, `amount`, `releaseTime`, `unbondingId` | An unstake. The delegator's event carries `releaseTime` and `unbondingId`; the validator's does not. |
| `stake.claimed` | `delegator`, `validator`, `amount`, `unbondingId` | A matured unbond is claimed (`StakeClaim`). |
| `stake.rewardsClaimed` | `addr`, `paidZNHB`, `periods`, `aprBps`, `nextEligibleUnix` | Rewards are claimed. |
| `stake.claimed` (rewards alias) | `addr`, `minted`, `periods`, `aprBps`, `nextEligibleUnix` | Also emitted on a rewards claim, under the same event type string as the unbond claim above (`StakeRewardsClaimed.LegacyEvent`). Distinguish the two by their attributes. |
| `stake.emissionCapHit` | `requestedZNHB`, `attemptedZNHB`, `allowedZNHB`, `ytd`, `cap` | The annual emission cap reduced a reward claim. |
| `stake.paused` | `addr`, `operation` (`delegate`, `undelegate`, `claim`, `claimRewards`), `reason`, `unbondingId` | A staking request was rejected because the module is paused. |
| `stake.validatorRegistrationChanged` | `addr`, `registered`, `at` | An account's validator registration flag changed. |
