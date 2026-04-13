# Treasury Operations Runbook

## KMS management

- **Key inventory** – Maintain a signed inventory of active mint signer KMS keys with owner,
  creation date, and rotation schedule.
- **Rotation cadence** – Rotate signer keys at least quarterly or after any security incident.
  Follow the signer rotation steps in the peg policy and verify `swap.mint.proof` events show the
  new `priceProofId`.
- **Access controls** – Restrict mint signer roles to the treasury security group. Enable hardware
  MFA for break-glass administrators.

## Break-glass procedures

1. **Trigger conditions** – Initiate break-glass when the custody oracle is unavailable, deviation
   exceeds approved limits, or proof freshness alarms fire for more than two windows.
2. **Activate manual feed** – Inject a manual quote via `SetSwapManualQuote` with explicit
   justification recorded in the incident ticket. Confirm subsequent mints emit `swap.mint.proof`
   events with `source=manual` and the temporary feeders listed.
3. **Pause minting** – If manual rates cannot be trusted, pause minting by disabling the token or
   revoking the provider in `swap.ProviderStatus` until market conditions normalize.
4. **Post-incident review** – Export the affected vouchers and burns, capturing `priceProofId` and
   TWAP artifacts for the audit trail. Document remediation and update dashboards/alerts to prevent
   recurrence.

## Operational checkpoints

- Daily: review `swap.redeem.proof` events to ensure treasury settlements include proof references.
- Weekly: reconcile dashboard freshness metrics with ledger `priceProofId` timestamps.
- Monthly: validate that KMS key rotation plans remain on schedule and break-glass tooling is
  functional.
