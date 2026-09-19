# POTSO Reward Payout Modes

The reward module has two settlement modes, chosen by `[potso.rewards].PayoutMode` (`auto` or `claim`). Both use the same budget, weights and winner selection ([potso_rewards.md](../potso_rewards.md)). They differ in when ZNHB leaves the treasury account. Code: `processPotsoRewardEpoch` (`core/state_transition.go`), `Node.PotsoRewardClaim` (`core/node.go`).

## Comparison

| Mode | Treasury movement | Claim record | Events |
| --- | --- | --- | --- |
| `auto` | Debited by `TotalPaid` at epoch processing; each winner is credited in the same step. | `claimed = true`, `mode = auto`, `claimedAt` = settlement block time. | `potso.reward.paid` per winner. |
| `claim` | Nothing moves at epoch processing. | `claimed = false`, `claimedAt = 0`, `mode = claim`. | `potso.reward.ready` per winner. |

In both modes a history entry is written when a payout is settled (auto: at epoch processing; claim: at claim time) and `potso.reward.epoch` is emitted once per epoch.

## Auto mode

The treasury is debited and winners are credited during block execution. If the treasury balance is below `EmissionPerEpoch`, the budget is `min(EmissionPerEpoch, balance)` and payouts shrink accordingly, down to no payouts for an empty treasury. The epoch is still marked processed.

## Claim mode

At epoch processing the node records, for each winner, the amount and `claimed = false`. The winner (or anyone holding a valid signature by the winner) then calls `potso_reward_claim` ([rewards-api.md](rewards-api.md)). `Node.PotsoRewardClaim` then:

1. fails with `ErrClaimingDisabled` ("claiming disabled") if the node's configured mode is not `claim`;
2. fails with `ErrRewardNotFound` ("reward not found") if no claim record exists for `(epoch, address)`;
3. returns `paid = false` and the amount, with no state change, if the claim is already settled;
4. fails with `ErrInsufficientTreasury` (`INSUFFICIENT_TREASURY`) if the treasury balance is below the amount, leaving the claim pending;
5. otherwise moves the amount from the treasury to the address, sets `claimed = true` and `claimedAt` (node wall-clock time), appends a history entry, and emits `potso.reward.paid` with `mode = claim`.

Claim mode does not reserve treasury funds. Each epoch's budget is computed from the current treasury balance, without subtracting claims that are still unpaid from earlier epochs.

`PotsoRewardClaim` reads the current configuration, so if the mode is switched from `claim` to `auto`, outstanding claim records can no longer be claimed (they fail with "claiming disabled").

## Configuration

```toml
[potso.rewards]
PayoutMode = "auto"        # or "claim"
TreasuryAddress = "znhb1..."
EmissionPerEpoch = "1000000000000000000"
MinPayoutWei = "1000000000000000"
```

`Config.PotsoRewardConfig()` normalises the mode: `claim` (any case) selects claim mode and every other value, including an empty one, selects `auto`. The mode is read at start-up; a restart with a different mode applies to epochs processed afterwards, and each stored claim keeps the mode it was created with. Key names, other keys and defaults: [config.md](config.md).

## Failure handling

| Scenario | Auto | Claim |
| --- | --- | --- |
| Treasury balance below the emission | Budget is reduced to the balance. | Budget is reduced to the balance at epoch time. Claims fail with `INSUFFICIENT_TREASURY` until the balance covers them. |
| Claim retried after success | Not applicable. | `paid = false`, amount returned. |
| Mode changed | New epochs use the new mode. Stored claims keep the mode they were written with. | Same, but see the note on `claiming disabled` above. |

The stored `mode` appears in `potso_rewards_history` and in the CSV from `potso_export_epoch`.
