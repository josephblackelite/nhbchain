# Treasury & KMS Runbooks

## Signer rotation

1. **Preparation**
   - Generate the replacement KMS key and validate the address offline.
   - Update the rotation checklist with operator approvals (dual-control required).
2. **Execution**
   - Load the new key into the payments gateway environment variable (`MinterKMSEnv`).
   - Restart the gateway; the rotating signer wrapper logs the old/new addresses.
   - Submit a canary mint to confirm the new signer matches the on-chain mint authority.
3. **Validation**
   - Call `swap_provider_status` and verify the `oracleFeeds` timestamps advance.
   - Archive signer history and revoke the previous key material.

## Burn receipt processing

- Burns must be captured via `swap_burn_list` and reconciled against treasury ledgers daily.
- The custody desk publishes receipts containing burn and treasury transaction identifiers.
- Operations confirm the linked vouchers move to `reconciled` and that `swap.treasury.reconciled` events are emitted.

## Break-glass procedure

1. Pause new mints by toggling the token mint authority or raising the `MaxQuoteAgeSeconds` to zero.
2. Switch feeder priority to the manual oracle and publish an operator-approved rate.
3. Notify governance and document the timeline in the incident tracker.

## Dual-control checklist

- KMS key rotations, burn receipt publications, and manual reconciliations require two operators (initiator + approver).
- All actions must reference ticket IDs and are logged in the audit channel.
- Weekly access reviews ensure only on-call operators retain signer permissions.
