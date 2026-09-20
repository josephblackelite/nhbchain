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

## How a device affects a transaction

A transaction may carry `deviceId` and `merchantAddr`. They are used in two places:

1. **Sponsorship evaluation.** `CheckPOSRegistry` (`core/tx/checks.go`) marks a sponsored
   transaction `throttled` when the device is revoked (`device sponsorship revoked`) or is
   bound to a different merchant (`device registered to merchant <address>`). A device with no
   record is not blocked.
2. **Per-device daily cap.** When `DeviceDailyTxCap` is set in `[global.Paymaster]`, a device
   may have at most that many sponsored transactions per UTC day, and a sponsored transaction
   without both `merchantAddr` and `deviceId` is throttled
   (see [paymaster budgets](./paymaster-budgets.md)).

## Operations

* Registering, revoking and restoring devices, and who may sign those transactions, are
  described in [POS merchant and device onboarding](./pos-onboarding.md) and
  [POS pause and revoke](./pos-pause-revoke.md).
* To check how a device would be treated, run `tx_previewSponsorship` (see
  [Paymaster administration](../launch/paymaster-admin.md)) with `merchantAddr` and `deviceId`
  set on the transaction.
* Certificate, HSM and firmware management are outside this repository's code.
