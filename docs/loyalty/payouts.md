# Base Spend Rewards

The base spend reward mints ZapNHB (ZNHB) to shoppers for every qualifying NHB
transaction. Rewards are sourced from the loyalty treasury and accrue alongside
program-specific payouts.

## Default Accrual Rate

* The chain-wide default accrual rate is **5,000 basis points (50%)** as defined by
  `loyalty.DefaultBaseRewardBps`, minting 0.5 ZNHB for every 1 NHB of qualifying
  spend.
* All basis-point math uses the `loyalty.BaseRewardBpsDenominator` constant of
  10,000.
* Operators can toggle the engine with the `Active` flag, but when enabled an
  explicit reward rate is no longer required—the default is applied whenever the
  stored config omits `BaseBps`.

For example, a 20,000 wei NHB payment mints 10,000 wei ZNHB: `20_000 * 5_000 / 10_000`.
Caps (per-transaction or daily) clamp the computed reward after the rate is
applied.

## Configuration Surface

The global configuration lives at `loyalty.GlobalConfig` and is stored on-chain
via `state.Manager.SetLoyaltyGlobalConfig`. Relevant fields:

| Field | Description |
|-------|-------------|
| `Active` | Enables/disables base reward accrual. |
| `Treasury` | 20-byte address that funds rewards. |
| `BaseBps` | Optional basis-point override; defaults to 5,000 when zero. |
| `MinSpend` | Minimum NHB spend required to earn the reward. |
| `CapPerTx` | Hard cap (in ZNHB) per transaction. |
| `DailyCapUser` | Daily cap (in ZNHB) per shopper. |

`Normalize()` now applies the default basis points before enforcing non-negative
caps and thresholds. Governance tooling and genesis loaders automatically apply
these defaults so nodes ingest consistent settings.

## Event Stream

Base rewards emit `loyalty.base.accrued` events with attributes:

| Attribute | Description |
|-----------|-------------|
| `day` | UTC day (`YYYY-MM-DD`). |
| `token` | Always `NHB`. |
| `amount` | Spend amount (wei). |
| `from` | Hex sender address. |
| `to` | Hex recipient address. |
| `reward` | Minted ZNHB (wei). |
| `baseBps` | Basis points used for the reward computation. |

When rewards are skipped (due to pauses, caps, treasury balance, etc.)
`loyalty.base.skipped` events continue to carry the `reason` and contextual
metadata to aid observability.

## Pauses and Caps

* `gov.v1.MsgSetPauses` toggles module-wide pauses; when active no base rewards
  are minted and no events are produced.
* `CapPerTx` truncates the computed reward before persistence.
* `DailyCapUser` clamps the total minted amount per shopper per UTC day; the
  remaining allowance is calculated with on-chain meters.

Operators should ensure the treasury maintains sufficient ZNHB liquidity. The
engine refuses to mint when the treasury balance falls below the requested
reward and emits a `treasury_insufficient` skip event.
