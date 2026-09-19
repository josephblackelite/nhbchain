# Creator Share Accounting

The creator engine (`native/creator/math.go`, `engine.go`) accounts for fan stakes with shares. Per creator the ledger tracks:

* `totalAssets`: NHB staked behind the creator (base units).
* `totalShares`: outstanding shares, including the bootstrap share.
* `indexRay`: `totalAssets * 1e27 / totalShares` (1e27 = `oneRay`); `1e27` when there are no shares, `0` when there are shares but no assets.

## Minting (stake)

* First deposit (no shares or no assets yet): the deposit must be at least `minDeposit` = 1,000 base units (`creator engine: deposit below minimum` otherwise). The engine mints `deposit - minLiquidity` shares to the fan and adds `minLiquidity` = 1 share to the pool's `totalShares` as a bootstrap share (it is counted in the total but not assigned to any account).
* Later deposits: `mintShares = floor(deposit * totalShares / totalAssets)`. If that is zero the call fails with `deposit below minimum` when `deposit < 1000`, otherwise `deposit too small for share precision`.
* Afterwards `totalAssets += deposit`, `totalShares += mintShares (+ the bootstrap share on the first deposit)` and `indexRay` is recomputed.

## Redemption (unstake)

The caller passes a number of shares (which must not exceed their stake's shares). `redeemAssets = floor(shares * totalAssets / totalShares)`; a result of zero fails with `creator engine: redeem value below precision`. If afterwards no more than the bootstrap share would remain (`remainingShares <= 1`) or no assets would remain, the residual assets are added to the payout, and the ledger's assets and shares reset to zero with `indexRay` set to `1e27`. The fan's stake record is deleted when its amount or shares reach zero.

## Yield and tips

* Tips add to the ledger's `PendingDistribution` one-for-one; they are not shares.
* Each stake attempts to add a yield of 2.5% of the deposit (`stakingAccrualBps` = 250) to `PendingDistribution` and `TotalStakingYield`, funded from the rewards-treasury account only if it holds enough NHB (see [`overview.md`](./overview.md)).

## Limits

* Content URI: at most 512 bytes, schemes `https`, `ipfs`, `ar`, `nhb`. Metadata: at most 4096 bytes, UTF-8; its BLAKE3-256 hash is stored on the content record.
* Staking per fan: at most 1,000,000,000,000 base units (`fanStakeEpochCap`) per 3600-second window.
* Tips: at most 5 per creator per rolling 1-second window (`tipRateBurst`, `tipRateWindowSeconds`).
* Zero or negative stake and tip amounts are rejected.

## Note on balances

In `StakeCreator` the fan's NHB balance is debited by the deposit and the amount is not credited to any account, and `UnstakeCreator` credits the fan's balance with the redeemed assets without debiting any account (`native/creator/engine.go`). The engine's stake path therefore does not conserve NHB supply. The module's write RPCs are disabled (see [`overview.md`](./overview.md)).
