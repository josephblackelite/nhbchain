# Device Attestation Runbook

This runbook defines the flow for enrolling, maintaining and revoking POS devices once a merchant has been approved. It assumes the merchant entry already exists in the registry and that sponsorship certificates can be issued through the mTLS CA pipeline.

## 1. Enrollment preparation

1. **Collect device metadata**
   * Unique device identifier (UUID or serial number).
   * Firmware version and build hash from the manufacturer.
   * Merchant slug and sponsorship certificate bundle.
2. **Validate prerequisites**
   * Ensure the merchant sponsorship caps are active by reviewing the latest registry snapshot export for the merchant.
   * Confirm the device firmware hash matches the approved baseline recorded in the asset management system.

## 2. Device registration

1. **Bind the device**
   * Create a `pos.v1.MsgRegisterDevice` payload that links the device identifier to the merchant and captures the firmware hash.
   * Submit the governance transaction through the standard multi-signer pipeline and record the resulting hash in the ticket.
2. **Issue an mTLS certificate**
   * Generate a device CSR using the HSM-backed provisioning tool.
   * Trigger the CA workflow to issue a device certificate chained to the merchant sponsorship profile.
3. **Install credentials**
   * Flash the certificate bundle to the secure element on the device.
   * Load the merchant private key alias and validate that the device can establish an mTLS session against the gateway QA endpoint: `openssl s_client -connect gateway.qa.nhb:443 -cert device.pem -key device.key -CAfile ca.pem`

## 3. Attestation renewal

1. **Monitor certificate expiry**
   * Check the expiring-certs Grafana panel weekly for devices approaching expiry within 14 days.
   * Schedule automated CSR regeneration and CA issuance for the affected devices.
2. **Rotate firmware**
   * When deploying new firmware, attach the signed firmware manifest to the change request and update the expected hash in the registry via `pos.v1.MsgUpdateDeviceFirmware`.
   * Re-run the attestation command to confirm the device reports the new hash.

## 4. Revocation

1. **Immediate revocation**
   * Submit `pos.v1.MsgRevokeDevice` when a device is lost, stolen or compromised.
   * Revoke the associated mTLS certificate via the CA interface and publish the CRL update.
2. **Graceful retirement**
   * For planned decommissions, schedule revocation after the final settlement batch and document the timeline in the ticket.
   * Update asset management records and reclaim any hardware tokens.

## 5. Post-change validation

* Confirm the device appears in the attested devices dashboard within 15 minutes of registration.
* Ensure alerts are configured for mTLS handshake failures and revocation CRL staleness.
* Capture logs from the gateway pod if attestation fails: `kubectl logs deploy/gateway --tail=200`
