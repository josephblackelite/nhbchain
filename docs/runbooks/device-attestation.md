# Device Registry Runbook

This page replaces the earlier "device attestation" runbook. The code has no device
attestation: there is no firmware hash, no firmware update message, no certificate issuance
or revocation, and no attested-device query. A device is a record in the POS registry, and
that record is all the chain knows about it.

## What the chain knows about a device

The record at `pos/device/<device id>` (`native/pos/registry.go`) holds:

* `DeviceID` (trimmed, case preserved),
* `Merchant` (the bound merchant address, trimmed and lowercased),
* `Revoked`,
* `Nonce`, `ExpiresAt` and `ChainID` from the last registry message that wrote it.

1. **Bind the device**
   * Create a `pos.v1.MsgRegisterDevice` payload that links the device identifier to the merchant (the message has no firmware-hash field; see `proto/pos/registry.proto`).
   * Submit the governance transaction through the standard multi-signer pipeline and record the resulting hash in the ticket.
2. **Issue an mTLS certificate**
   * Generate a device CSR using the HSM-backed provisioning tool.
   * Trigger the CA workflow to issue a device certificate chained to the merchant sponsorship profile.
3. **Install credentials**
   * Flash the certificate bundle to the secure element on the device.
   * Load the merchant private key alias and validate that the device can establish an mTLS session against your gateway endpoint: `openssl s_client -connect <gateway-host>:443 -cert device.pem -key device.key -CAfile ca.pem`

A transaction may carry `deviceId` and `merchantAddr`. They are used in two places:

1. **Monitor certificate expiry**
   * Check the expiring-certs Grafana panel weekly for devices approaching expiry within 14 days.
   * Schedule automated CSR regeneration and CA issuance for the affected devices.
2. **Rotate firmware**
   * When deploying new firmware, attach the signed firmware manifest to the change request. The code has no firmware-hash field or `MsgUpdateDeviceFirmware` message, so the expected hash must be tracked outside the on-chain registry.
   * Re-run the attestation command to confirm the device reports the new hash.

## Operations

* Registering, revoking and restoring devices, and the current state of those transaction
  types, are described in [POS merchant and device onboarding](./pos-onboarding.md) and
  [POS pause and revoke](./pos-pause-revoke.md).
* To check how a device would be treated, run `tx_previewSponsorship` (see
  [Paymaster administration](../launch/paymaster-admin.md)) with `merchantAddr` and `deviceId`
  set on the transaction.
* Certificate, HSM and firmware management are outside this repository's code.
