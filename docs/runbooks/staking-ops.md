# Staking Emission Operations

This runbook covers the operational tooling for tracking staking reward emissions and responding to the annual cap that is enforced on ZapNHB payouts.

## Monitor year-to-date emissions

* The cumulative amount minted in a calendar year is stored under the key `staking/emissions/<YYYY>` in the application state. You can inspect it with the state manager, e.g.:
  * `statectl kv get staking/emissions/2024`
  * `nhbctl staking emission-ytd --year 2024`
* The value is updated every time a staking reward is minted, so an increase without any matching claim indicates a bug that should be escalated.
* During roll-over into a new year a fresh key is used automatically. No manual reset is required, but it is good practice to validate the new key once the first payout in January settles.

## Understand the emission cap event

* When a claim would exceed the configured `staking.maxEmissionPerYearWei` parameter the protocol mints only the remaining headroom and emits a `stake.emissionCapHit` event.
* The event attributes show the calendar year, the amount minted, and the remaining headroom (typically `0`). Set up alerts on this event so the on-call operator is notified immediately.
* Delegators can continue to claim once the calendar year rolls over or governance raises the cap; unminted residual rewards remain accrued on-chain.

## Respond to cap saturation

1. Confirm the current year-to-date total and the recent `stake.emissionCapHit` events.
2. Decide whether to raise `staking.maxEmissionPerYearWei` through a governance proposal or to leave the cap in place. Coordinate with treasury and policy stakeholders before making changes.
3. If the cap is increased, submit the governance proposal with the new integer value in wei. After execution, verify that subsequent claims no longer emit the cap-hit event.
4. Document the incident in the ops log, including the amount minted at the cap and any remediation timeline shared with the community.

## Troubleshooting checklist

* If cap-hit events are emitted earlier than expected, double check that the configured value matches the approved budget (no stray whitespace or units).
* When operators believe the cap should have reset, inspect both the previous and current year keys to confirm the rollover.
* For persistent discrepancies, collect the relevant event stream and state snapshots and escalate to the protocol team for deeper analysis.
