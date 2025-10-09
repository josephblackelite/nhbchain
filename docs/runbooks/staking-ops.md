# Staking Emission Operations

This runbook covers the operational tooling for tracking staking reward emissions and responding to the annual cap that is enforced on ZapNHB payouts.

## Monitor year-to-date emissions

* The cumulative amount minted in a calendar year is stored under the key `staking/emissions/<YYYY>` in the application state. You can inspect it with the state manager, e.g.:
  * `statectl kv get staking/emissions/2024`
  * `nhbctl staking emission-ytd --year 2024`
* The value is updated every time a staking reward is minted, so an increase without any matching claim indicates a bug that should be escalated.
* During roll-over into a new year a fresh key is used automatically. No manual reset is required, but it is good practice to validate the new key once the first payout in January settles.
* Dashboards: The **Staking Program Overview** Grafana dashboard surfaces "Emission YTD" and "Monthly Payout" panels. Keep both visible in the NOC rotation. If the YTD plot trends towards the configured cap faster than expected, begin coordinating with treasury.

## Understand the emission cap event

* When a claim would exceed the configured `staking.maxEmissionPerYearWei` parameter the protocol mints only the remaining headroom and emits a `stake.emissionCapHit` event.
* The event attributes show the calendar year, the amount minted, and the remaining headroom (typically `0`). Set up alerts on this event so the on-call operator is notified immediately.
* Delegators can continue to claim once the calendar year rolls over or governance raises the cap; unminted residual rewards remain accrued on-chain.

## Pause behaviour

* Governance can halt staking mutations by toggling `Pauses.Staking=true`. The node rejects delegate, undelegate, unbond-claim, and reward-claim flows with JSON-RPC error code `-32050` and the `staking module paused` message.
* Every rejected request appends a `stake.paused` event that captures the delegator address, operation (`delegate`, `undelegate`, `claim`, `claimRewards`), and the reason (`paused by governance`). Unbond claims also include the `unbondingId` for observability.
* Existing delegations continue accruing index updates and unbonding timers keep progressing, but the assets remain locked until the pause is lifted. Operators should communicate to delegators that matured unbonds and rewards will become claimable again once governance resumes the module.
* Read-only staking helpers (`stake_previewClaim`, `stake_getPosition`) also return HTTP `503` while the pause is active. Downstream tooling should treat this as a temporary outage rather than a permanent failure.
* Dashboard alerts named **Staking Pause 80/90/100%** light up when total staked approaches the configured cap. Verify whether the pause was intentional before taking recovery actions.

### Pause handling checklist

1. Confirm the pause via the `stake.paused` event stream and the governance proposal that toggled `staking.pause.enabled`.
2. Publish a community update summarising the reason, scope (delegate/undelegate/claim), and expected timeline for reactivation.
3. Disable automation that retries failed staking transactions to avoid log noise.
4. Track pending unbonds approaching maturity; once the pause lifts, proactively message the impacted delegators to claim.

## Respond to cap saturation

1. Confirm the current year-to-date total and the recent `stake.emissionCapHit` events.
2. Decide whether to raise `staking.maxEmissionPerYearWei` through a governance proposal or to leave the cap in place. Coordinate with treasury and policy stakeholders before making changes.
3. If the cap is increased, submit the governance proposal with the new integer value in wei. After execution, verify that subsequent claims no longer emit the cap-hit event.
4. Document the incident in the ops log, including the amount minted at the cap and any remediation timeline shared with the community.

## Troubleshooting checklist

* If cap-hit events are emitted earlier than expected, double check that the configured value matches the approved budget (no stray whitespace or units).
* When operators believe the cap should have reset, inspect both the previous and current year keys to confirm the rollover.
* For persistent discrepancies, collect the relevant event stream and state snapshots and escalate to the protocol team for deeper analysis.

## Safe parameter changes

* **Approvals**: Secure treasury and governance committee sign-off before proposing updates to any `staking.*` parameter. Document the motivation, risk mitigation, and expected downstream impact.
* **Dry runs**: Run `go test ./services/staking/...` locally with the proposed values configured via environment overrides to confirm no unit tests regress. Use the staging network to validate index progression when adjusting APR or payout interval.
* **Rollout**: Sequence changes so that scale-sensitive parameters (`staking.rewardIndexScale`, `staking.payoutIntervalSeconds`) execute during low-traffic windows. Monitor the Grafana dashboard panels for total staked, pending rewards, and emission YTD immediately after execution.
* **Back-out plan**: Prepare a follow-up proposal that reverts to the prior value in case the new configuration causes unexpected behaviour. Keep the diff ready to submit if alerts fire.

## Interpret staking events

* `stake.delegated` / `stake.undelegated`: Validate that the `amount` lines up with transaction intents and the `validator` matches the expected operator. Spikes in delegation volume without matching announcements may signal compromised accounts.
* `stake.claimed`: Cross-check with emission YTD to ensure the minted rewards reconcile with the reward index advance for the period.
* `stake.rewardIndexAdvanced`: Fired at every 30-day payout. Confirm that the delta equals `(targetAprBps/12) * rewardIndexScale` and that no validators are paused.
* `stake.emissionCapHit`: Triggers the "Emission Cap 100%" alert. Escalate to treasury immediately and follow the cap saturation runbook section above.
* `stake.paused`: Indicates governance toggled the module. Verify the `reason` attribute and correlate with the pause handling checklist.
