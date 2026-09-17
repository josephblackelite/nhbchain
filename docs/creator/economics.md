# Creator Economy Share Accounting

The creator engine mints proportional staking shares using an ERC4626-style
index. The engine tracks three aggregate values per creator:

* **`totalAssets`** – the amount of NHB staked behind the creator.
* **`totalShares`** – the outstanding fan shares, including any bootstrap
  liquidity held by the protocol.
* **`indexRay`** – a high-precision (1e27) price index equal to
  `totalAssets * 1e27 / totalShares`.

## Minting

When a fan stakes `deposit` NHB the engine calculates the number of shares to
mint as:

```
mintShares = floor(deposit * totalShares / totalAssets)
```

If this is the first deposit (`totalShares == 0`) the engine performs a
bootstrap by minting `MIN_LIQUIDITY` shares to the zero address and allocating
`deposit - MIN_LIQUIDITY` shares to the fan. The current constants are
`MIN_LIQUIDITY = 1` share and `MIN_DEPOSIT = 1_000` NHB. Deposits smaller than
`MIN_DEPOSIT` are rejected, and any deposit that would round down to zero shares
is rejected with `deposit too small for share precision`.

After minting, the ledger updates `totalAssets`, `totalShares`, and recomputes
`indexRay`.

## Redemption

Unstaking burns a share amount and returns assets according to:

```
redeemAssets = floor(shares * totalAssets / totalShares)
```

If the redemption would drain the pool the residual assets (including the
bootstrap share) are returned to the redeemer and the ledger resets its
aggregates to zero.

The engine enforces that a fan cannot withdraw more than their pro-rata share of
assets. Property tests cover dilution scenarios to ensure no actor can exit with
more than their proportional stake.

## Metadata and Anti-Grief Controls

* Content URIs must use an allow-listed scheme (`https`, `ipfs`, `ar`, or `nhb`),
  be UTF-8, and not exceed length limits. Metadata is trimmed, validated as
  UTF-8, and hashed with BLAKE3 (stored on the content record).
* Staking per fan is capped per epoch (`1h`) by `fanStakeEpochCap`.
* Tips per creator are rate limited using a fixed 1-second window and
  `tipRateBurst` allowance.
* Zero-value stakes or tips are rejected early to prevent spam.
