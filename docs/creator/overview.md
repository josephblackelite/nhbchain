# Creator Module Overview

The creator module (`native/creator`) models content publication, tips and fan staking behind a creator. Code: `native/creator/engine.go`, `types.go`, `events.go`, `math.go`, the node wrappers in `core/node.go` (`Node.Creator*`) and the RPC handlers in `rpc/creator_handlers.go`.

## Availability

* There is **no creator transaction type**. The only way in the repository to run the engine's write operations is the RPC handlers, and those are disabled: `creator_publish`, `creator_tip`, `creator_stake`, `creator_unstake` and `creator_payouts` with `claim: true` return HTTP 410 with error code `-32060` (`creatorRPCDisabledMessage`).
* The read path of `creator_payouts` (with `claim` omitted or false) is live and requires RPC authentication.
* As a result no creator state can currently be created on a running chain through the node's public interfaces. The engine and its rules are described below because they are present in the code; see [`api.md`](./api.md) for the RPC surface.

## Core concepts (`native/creator/types.go`)

| Concept | Description |
| --- | --- |
| **Content** | `{ID, Creator, URI, Metadata, Hash, PublishedAt, TotalTips, TotalStake}`. `Hash` is the hex BLAKE3-256 of the trimmed metadata string. |
| **Tip** | NHB moved from a fan to the module payout vault, recorded against a piece of content and added to the creator's pending payout. |
| **Stake** | `{Creator, Fan, Amount, Shares, StakedAt, LastAccrual}`: a fan's position behind a creator, measured in shares. |
| **Payout ledger** | Per creator: `TotalTips`, `TotalStakingYield`, `PendingDistribution`, `LastPayout`, `TotalAssets`, `TotalShares`, `IndexRay`. |

The payout vault is a module address derived from the seed `module/creator/payout`; the staking-yield source is the module address derived from `module/creator/rewards` (`core/node.go`).

## State layout (`core/state/manager.go`)

* `creator/content/<contentId>`: content record.
* `creator/stake/<creator>/<fan>`: stake position.
* `creator/ledger/<creator>`: payout ledger.
* A snapshot of the rate-limit windows is also persisted (`CreatorRateLimitPut`) so limits survive restarts.

## Engine behavior

* **Publish** (`PublishContent`): content ID is trimmed and must be non-empty and not already used (`creator engine: content already exists`). URI must be at most 512 bytes and use the scheme `https`, `ipfs`, `ar` or `nhb`. Metadata is trimmed, at most 4096 bytes and valid UTF-8. Emits `creator.content.published`.
* **Tip** (`TipContent`): amount must be positive; the payout vault must be configured; the content must exist. The fan's NHB balance is debited and the payout vault credited; content `TotalTips`, ledger `TotalTips` and `PendingDistribution` increase by the amount. A creator can receive at most 5 tips per rolling 1-second window (`creator engine: tip rate limit exceeded`). Emits `creator.content.tipped` and `creator.payout.accrued`.
* **Stake** (`StakeCreator`): amount must be positive; a fan can stake at most `fanStakeEpochCap` = 1,000,000,000,000 base units in any window that starts at its first stake and rolls after 3600 seconds (`creator engine: per-epoch stake cap exceeded`). Shares are minted as described in [`economics.md`](./economics.md). A yield of `deposit * 250 / 10000` (2.5%) is moved from the rewards-treasury account to the payout vault and added to the ledger's `TotalStakingYield` and `PendingDistribution`, only if the treasury account's NHB balance covers it; otherwise the yield is zero and the stake still succeeds. Emits `creator.fan.staked` and, if a yield was paid, `creator.payout.accrued`.
* **Unstake** (`UnstakeCreator`): the amount is a number of **shares**, not NHB. The NHB paid out is computed from the ledger's assets and shares (see [`economics.md`](./economics.md)). Emits `creator.fan.unstaked` with the assets returned.
* **Claim** (`ClaimPayouts`): moves `PendingDistribution` from the payout vault to the creator's NHB balance (fails with `creator engine: payout vault underfunded` if the vault is short), zeroes it and sets `LastPayout`. Emits `creator.payout.accrued`.

All amounts are NHB base units (18 decimals).

## Events

Addresses in event attributes are `0x` plus lowercase hex of the 20 address bytes.

| Event | Attributes |
| --- | --- |
| `creator.content.published` | `contentId`, `creator`, `uri` |
| `creator.content.tipped` | `contentId`, `creator`, `fan`, `amount` |
| `creator.fan.staked` | `creator`, `fan`, `amount`, `shares` (the fan's total shares after the stake) |
| `creator.fan.unstaked` | `creator`, `fan`, `amount` (NHB returned) |
| `creator.payout.accrued` | `creator`, `pending`, `totalTips`, `totalYield` |
