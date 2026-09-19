# POTSO and Consensus

This page states what the BFT engine (`consensus/bft/bft.go`) actually reads, and how that relates to POTSO. The short version: quorum voting power is stake-based only, and proposer selection adds a capped boost from the account-level engagement score. The POTSO daily meters, the epoch weight snapshot, and the POTSO stake locks are not inputs to the BFT engine.

## Voting power (quorums)

The engine gets voting power from `NodeInterface.GetValidatorSet()`, which returns a copy of `StateProcessor.ValidatorSet` (`Node.GetValidatorSet`, `core/node.go`). Each map value is the validator's eligibility basis stored by `setAccount` / `applyValidatorSelection` (`core/state_transition.go`, `core/epochs.go`). The basis is the account's `Stake` including ZNHB delegated in by third parties (`validatorEligibilityBasis`). A validator must be registered, be self-delegated, and meet `staking.minimumValidatorStake` to enter `EligibleValidators` (`setAccount`). See [validator onboarding](../validators/onboarding.md).

Addresses are added to `ValidatorSet`, and the stored values refreshed, only at epoch boundaries (`finalizeEpoch` when `height % epochConfig.Length == 0`, `core/epochs.go`), plus a start-up liveness repair (`ensureValidatorSetLiveness`, only when the set is empty) and a legacy-account migration. The one immediate change is removal: `setAccount` deletes an address from `ValidatorSet` as soon as it stops meeting the registration, stake and self-delegation gate. At the epoch-boundary recomputation a validator must additionally pass the heartbeat liveness gate:

- `validatorReadyForActivation` (`core/epochs.go`) requires the account to be `ValidatorRegistered` and `EngagementLastHeartbeat` to be non-zero, not more than 2 minutes after the block time, and no older than `validatorReadinessGracePeriod`. That period is the larger of 15 minutes (`validatorReadinessMinGrace`) and 5 times `engagementConfig.HeartbeatInterval` (`validatorReadinessHeartbeatMultiple`; the default interval is 1 minute, so 15 minutes).
- `EngagementLastHeartbeat` is written by `applyHeartbeat` when a `TxTypeHeartbeat` (`0x08`) transaction is applied, so a validator that stops sending heartbeats is dropped at the next epoch boundary. It is not touched by the POTSO meter or by the (disabled) `potso_heartbeat` RPC.
- Without rotation, `applyValidatorSelection` builds the set from `EligibleValidators` and drops every entry that fails `validatorReadyForActivation`. With rotation, `computeEpochWeights` applies the same gate, together with the stake and self-delegation checks, before `selectValidators` picks the set.
- If the result is empty, `fallbackValidatorSet` refills it from the current set, `EligibleValidators` and the previous epochs' selections. That fallback skips the recency check but still requires registration, self-delegation, the minimum stake and a non-zero `EngagementLastHeartbeat` (a validator that has never sent a heartbeat is never resurrected).

At the start of every round `startNewRound` reloads the set and recomputes `totalVotingPower` as the sum of all non-nil values. A nil weight counts as 0. A vote from an address that is not in the set adds 0 power.

Votes are tracked per type (prevote, precommit) and per validator address. A second vote of the same type from the same validator in the same round is ignored (`addVoteIfRelevant`).

### Quorum threshold

`hasTwoThirdsPowerLocked` (and `verifyPolkaProofLocked` for proofs) uses:

```
threshold = floor((2 * totalVotingPower + 2) / 3)
reached   = accumulatedPower >= threshold
```

If `totalVotingPower` is 0 or unset, no quorum is ever reached.

## Proposer selection

`selectProposer(round)` (`consensus/bft/bft.go`):

1. Sort the validator-set keys.
2. For each validator, load its account (`GetAccount`) and compute `power = account.Stake + engagementWeightBoost(account.Stake, account.EngagementScore)`. Validators whose account cannot be loaded are skipped.
3. If every power is 0, pick `validators[round % len(validators)]`.
4. Otherwise compute `seed = SHA-256(lastCommitHash || round as 8-byte big-endian)`, take `seed mod totalPower`, and walk the validators in sorted order subtracting each power until the remainder falls inside one validator's power.

`engagementWeightBoost` adds at most `maxEngagementBoostBps = 2000` basis points (20%) of the validator's own `Stake`. It scales linearly with `EngagementScore`, capped at `engagement.DefaultConfig().DailyCap` (250):

```
boost = stake * min(score, DailyCap) * 2000 / (DailyCap * 10000)
```

For a member of the validator set the two figures are the same number: `validatorEligibilityBasis` (`core/state_transition.go`) returns `account.Stake` unchanged for a self-delegated account, and accounts that delegate to a different validator are excluded from the set. The difference is timing only. The `ValidatorSet` value is only refreshed at epoch boundaries, while proposer selection reads the live `account.Stake`, which can have changed since.

`EngagementScore` is the account field maintained by `rolloverEngagement` in `core/state_transition.go` from the account's daily engagement counters (`core/engagement`; minutes from `TxTypeHeartbeat`, transaction, escrow and governance counts, smoothed by an EMA). It is not the POTSO `Meter.score` and is not derived from `potso/meter/...` records.

## Validator selection

When rotation is enabled, `computeEpochWeights` ranks eligible validators by `epoch.ComputeCompositeWeight` (stake times `StakeWeight` plus account `EngagementScore` times `EngagementWeight`, `core/epoch/types.go`). The resulting `ValidatorSet` values are still the stake basis (`applyValidatorSelection` stores `basis`).

## What POTSO state does feed elsewhere

- The reward-epoch weight snapshot (`snapshots/potso/<epoch>/weights`) is read by governance vote casting, not by BFT. See [weights](weights.md) and [config](config.md).
- `TxTypeSubmitEvidence` records and their penalty processing run in block execution. See [evidence-and-penalties](evidence-and-penalties.md).
