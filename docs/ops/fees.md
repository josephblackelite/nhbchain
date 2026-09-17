# Fee operations runbook

This playbook outlines how to monitor and adjust the fee policy after the
introduction of monthly free-tier windows.

## Configuration knobs

Fee settings live under the `global.fees` section of the runtime configuration
and support the following fields:

| Field | Description | Default |
| --- | --- | --- |
| `freeTierTxPerMonth` | Number of transactions each payer receives per UTC calendar month before MDR applies. | `100` |
| `mdrBasisPoints` | Merchant discount rate assessed on the paid leg once the free tier is exhausted. | `150` |
| `ownerWallet` | Hex-encoded address that accrues routed MDR fees for the domain. | `""` |

Updates should be proposed via the governance parameter pipeline. Nodes log a
warning if the free-tier allowance is missing when a policy is loaded; this
backfill is expected for legacy records and can be ignored once the policy has
been resubmitted with the new fields populated.

## Monitoring

* **Free-tier depletion:** subscribe to `fees.applied` events and watch the
  `freeTierRemaining` attribute. Alerts should fire when large merchants fall
  below 10 remaining transactions to allow proactive top-ups.
* **Owner wallet accrual:** the `ownerWallet` field on each event identifies the
  treasury receiving MDR. Reconcile the balance against treasury statements at
  the end of each month.
* **Window resets:** the `windowStartUnix` attribute marks the beginning of the
  billing month. Confirm that events emitted after UTC midnight on the first of
  each month advertise the new window and reset usage counts back to one.

## Rollout checklist

1. Deploy nodes with the updated binary.
2. Resubmit the fee policy via governance to explicitly set
   `freeTierTxPerMonth`, `mdrBasisPoints`, and `ownerWallet` for each domain.
3. Verify that warning logs disappear after the policy update propagates.
4. Confirm that event consumers and dashboards ingest the new attributes before
   switching alert thresholds to the 100-transaction monthly limit.
