# POTSO Reward Payout Modes

The reward module has two settlement modes, chosen by `[potso.rewards].PayoutMode` (`auto` or `claim`). Both use the same budget, weights and winner selection ([potso_rewards.md](../potso_rewards.md)). They differ in when ZNHB leaves the treasury account. Only auto mode works end to end on a running node. Code: `processPotsoRewardEpoch` (`core/state_transition.go`), `Node.PotsoRewardClaim` (`core/node.go`).

## Comparison

| Mode | Treasury movement | Claim record | Events |
| --- | --- | --- | --- |
| `auto` | Debited by `TotalPaid` at epoch processing; each winner is credited in the same step. | `claimed = true`, `mode = auto`, `claimedAt` = settlement block time. | `potso.reward.paid` per winner. |
| `claim` | Nothing moves at epoch processing, or later (no working claim path). | `claimed = false`, `claimedAt = 0`, `mode = claim`. | `potso.reward.ready` per winner. |

A history entry is written when a payout is settled: in auto mode at epoch processing, in claim mode never on a running node (see below). `potso.reward.epoch` is emitted once per epoch in both modes.

## Auto mode

The treasury is debited and winners are credited during block execution. If the treasury balance is below `EmissionPerEpoch`, the budget is `min(EmissionPerEpoch, balance)` and payouts shrink accordingly, down to no payouts for an empty treasury. The epoch is still marked processed. When the treasury is the chain's admin wallet, the ZNHB Reward Pool ledger is reduced by the amount paid to other addresses in the same step (`processPotsoRewardEpoch`, and `withTreasuryPoolBooking` around `maybeProcessPotsoRewards` in `ProcessBlockLifecycle`, `core/epochs.go`), so the supply invariant holds.

## Claim mode

At epoch processing the node records, for each winner, the amount and `claimed = false`, and emits `potso.reward.ready`. Nothing then pays these records on a running node:

- The only RPC that settled them, `potso_reward_claim`, is retired and answers HTTP 410 (code `-32060`) ([rewards-api.md](rewards-api.md)). `nhb-cli potso reward claim` is retired too.
- `Node.PotsoRewardClaim` (`core/node.go`) still exists and is exercised by tests, but no RPC or CLI calls it. Its comment says it must not be exposed again, because it writes to the validator's live state outside block execution.
- No transaction type settles a claim.

A claim-mode epoch therefore records amounts but moves no ZNHB, adds no `potso_rewards_history` entry and emits no `potso.reward.paid`. The treasury is never debited in claim mode at epoch time either, so the budget of each later epoch is computed from the unchanged balance.

What `Node.PotsoRewardClaim` does when called in-process, for reference: it fails with `ErrClaimingDisabled` if the configured mode is not `claim`, with `ErrRewardNotFound` if no claim record exists for `(epoch, address)`, returns `paid = false` and the amount if the record is already claimed, fails with `ErrInsufficientTreasury` if the treasury balance is below the amount, and otherwise moves the amount from the treasury to the address, sets `claimed = true` with `claimedAt` from the node's wall clock, appends a history entry and emits `potso.reward.paid` with `mode = claim`. It reads the current configuration, so after a switch from `claim` to `auto` outstanding claim records fail with "claiming disabled".

## Configuration

```toml
[potso.rewards]
PayoutMode = "auto"        # or "claim"
TreasuryAddress = "znhb1..."
EmissionPerEpoch = "1000000000000000000"
MinPayoutWei = "1000000000000000"
```

`Config.PotsoRewardConfig()` normalises the mode: `claim` (any case) selects claim mode and every other value, including an empty one, selects `auto`. Both `config.toml` and `config/prod.toml` in the repository set `auto`. The mode is read at start-up; a restart with a different mode applies to epochs processed afterwards, and each stored claim keeps the mode it was created with. Key names, other keys and defaults: [config.md](config.md).

## Failure handling

| Scenario | Auto | Claim |
| --- | --- | --- |
| Treasury balance below the emission | Budget is reduced to the balance. | Budget is reduced to the balance at epoch time. Amounts are recorded but never paid. |
| Mode changed | New epochs use the new mode. Stored claims keep the mode they were written with. | Same. |

The stored `mode` appears in `potso_rewards_history` and in the CSV from `potso_export_epoch`.
