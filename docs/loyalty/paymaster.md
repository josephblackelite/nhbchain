# Loyalty Paymaster Health & Throttling

The loyalty engine requires businesses to pre-fund a paymaster pool in ZapNHB (ZNHB).
This document describes the runtime safeguards that protect reward execution when paymaster
funding approaches exhaustion.

## Reserve enforcement

* Each business can configure a **`paymaster_reserve_min`** threshold. When a reward would drop
  the paymaster below this minimum the engine throttles the accrual and returns the status
  `"throttled — low reserve"`. The transaction itself continues to settle; only the reward is
  skipped.
* Early-warning events (`loyalty.program.paymaster_warning`) fire when the projected balance after
  paying a reward falls to **80% headroom** above the configured reserve. The event attributes include
  the projected balance and reserve target to simplify alerting.
* The engine continues to enforce spend token `NHB` and reward token `ZNHB` exclusively. Future
  multi-asset support is intentionally disabled until downstream accounting and compliance flows are
  extended.

## Reward caps

Program reward configuration now supports additional throttles:

| Field | Description |
|-------|-------------|
| `DailyCapProgram` | Maximum total ZNHB a program can issue per UTC day across all recipients. |
| `EpochCapProgram` / `EpochLengthSeconds` | Maximum ZNHB per custom epoch window. Epoch accounting stops at the configured cap until the epoch rolls over. |
| `IssuanceCapUser` | Lifetime issuance ceiling per address for the program. |

All caps apply after per-transaction and per-user daily limits. When a cap blocks a reward the
engine emits `loyalty.program.skipped` with reason `daily_program_cap_reached`, `epoch_cap_reached`,
`issuance_cap_reached`, or `throttled — low reserve` depending on the guard that triggered.

## Observability

The following events surface paymaster health to off-chain monitors:

* `loyalty.program.paymaster_warning` – emitted when a payout would leave the paymaster within 20%
  of its configured reserve minimum (80% of the safety buffer consumed). Attributes include
  `balance`, `reserveMin`, and standard program metadata.
* `loyalty.program.skipped` – existing event now reports the human-readable reason string described
  above plus contextual metadata (`available`, `reserveMin`, etc.) when throttling is activated.

The new meters exposed via RPC/state (`loyalty.programDailyTotalAccrued`,
`loyalty.programEpochAccrued`, and `loyalty.programIssuanceAccrued`) allow operators to reconcile
cap usage and build dashboards.

## Developer notes

* Reserve enforcement and caps are designed to be **non-fatal** – they never revert the underlying
  settlement. Businesses should subscribe to the warning/skip events or poll the meters to
  proactively top up paymasters.
* All new fields accept zero values to disable the guard. Governance tooling must provide
  non-zero `EpochLengthSeconds` when `EpochCapProgram` is configured; otherwise program updates are
  rejected.
* Multi-token spend/reward combinations remain out of scope for this release. Any attempt to
  configure non-`NHB`/`ZNHB` symbols continues to raise `token_not_supported` or
  `reward_token_not_supported` events.
