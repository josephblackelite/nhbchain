# Merchant Onboarding Runbook

This runbook covers the operational workflow for onboarding new POS merchants onto the network registry. It complements the governance procedures in [pos-onboarding.md](./pos-onboarding.md) by documenting KYC intake, registry mutation and sponsorship configuration for operations engineers.

## 1. Intake & KYC verification

1. **Collect documentation**
   * Government-issued business registration or trade licence.
   * Proof of address dated within the last 90 days.
   * Authorised signer identification and contact information.
2. **Screen the merchant**
   * Run the entity through your sanctions/watchlist provider and record the reference ID in the ticketing system.
   * Confirm there are no existing active merchants with the same tax identifier by querying the registry inventory dashboard or exporting the nightly registry snapshot from the data warehouse.
3. **Approve or reject**
   * If the merchant is rejected, document the reason and close the ticket.
   * If approved, capture the merchant name, tax ID, settlement account and desired sponsorship limits for the registry mutation step.

## 2. Registry entry

1. **Stage the governance transaction**
   * Draft a `pos.v1.MsgRegisterMerchant` payload with the merchant metadata.
   * Store it as JSON in your change request repository (for example `ops/requests/pos/merchant-<slug>.json`).
2. **Submit the transaction**
   * Use the governance submission pipeline to broadcast via the operations key (attach the JSON payload to the change request and follow the standard multi-signer approval flow).
   * Capture the transaction hash in the ticket.
3. **Verify the record**
   * Poll the registry telemetry job until the merchant appears in the snapshot export.
   * Confirm the sponsorship flag is enabled and the metadata matches the approved values in the dashboard.

## 3. Sponsorship caps

1. **Set merchant-level caps**
   * Draft a `pos.v1.MsgUpdateMerchantCaps` mutation that encodes the per-day sponsorship cap and optional per-transaction cap.
   * Submit through the same governance flow as above and record the transaction hash.
2. **Validate limits**
   * Confirm the merchant snapshot export shows the new caps.
   * Update the monitoring dashboard (Grafana `POS Merchants` panel) with the new limits.

## 4. Key distribution

1. **Prepare signing keys**
   * Generate the merchant POS signing keypair using the hardware security module (HSM) and export the certificate signing request (CSR).
   * Store the CSR in the secure vault under the merchant slug.
2. **Issue sponsorship certificates**
   * Trigger the mTLS certificate authority workflow from the `ca-issuer` deployment using the standard change request template (reference the merchant slug).
   * Retrieve the issued certificate bundle from the vault and share it with the device provisioning team using the secure file transfer process.
3. **Record completion**
   * Update the merchant ticket with the certificate serial number and HSM key alias.
   * Hand off to the device attestation runbook for per-device enrollment.

## 5. Post-onboarding checks

* Verify the merchant appears on the daily reconciliation report.
* Confirm alerts exist for cap exhaustion in the paymaster budget dashboard.
* Schedule a 30-day review to ensure transaction volume matches expectations.
