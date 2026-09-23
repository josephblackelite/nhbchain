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

The registry messages (`proto/pos/registry.proto`) that create or change a device are
`MsgRegisterDevice` (fields `authority`, `merchant_addr`, `device_id`, `nonce`, `expires_at`,
`chain_id`), `MsgRevokeDevice` and `MsgRestoreDevice`. None has a firmware-hash field.
They are carried by `TxTypePOSRegistry` transactions; who may send them, and what each one
does, is in [POS merchant and device onboarding](./pos-onboarding.md) and
[POS pause and revoke](./pos-pause-revoke.md).

## Where a transaction's `deviceId` and `merchantAddr` are used

A transaction may carry `deviceId` and `merchantAddr` (`core/types/transaction.go`). The
code reads them in three places:

1. **Sponsorship evaluation.** `EvaluateSponsorship` looks the pair up in the registry
   (`CheckPOSRegistry`, `core/tx/checks.go`) and throttles a sponsored transaction when the
   merchant is paused, the device is revoked (`device sponsorship revoked`) or the device is
   bound to a different merchant (`device registered to merchant <address>`). A device or
   merchant with no record is not blocked. See [Paymaster budget](./paymaster-budgets.md).
2. **Per-device daily cap.** It also keeps per-merchant and per-device daily counters for the
   paymaster caps: when `DeviceDailyTxCap` is set in `[global.Paymaster]`, a device may have at
   most that many sponsored transactions per UTC day, and a sponsored transaction without both
   `merchantAddr` and `deviceId` is throttled (see [paymaster budgets](./paymaster-budgets.md)).
3. **Fee domain.** For an NHB or ZNHB transfer, `merchantAddr` is also the domain name the fee
   policy looks up. See [Fees and throttles](./fees-and-throttles.md).

A device or merchant that is not in the registry is not refused by the registry check: only a
record that exists and is paused, revoked or bound elsewhere changes the outcome.

## Operations

* Registering, revoking and restoring devices, and who may sign those transactions, are
  described in [POS merchant and device onboarding](./pos-onboarding.md) and
  [POS pause and revoke](./pos-pause-revoke.md).
* To check how a device would be treated, run `tx_previewSponsorship` (see
  [Paymaster administration](../launch/paymaster-admin.md)) with `merchantAddr` and `deviceId`
  set on the transaction.
* To read the record, use a tool that opens the state trie (`Manager.POSGetDevice`); no RPC
  method returns registry records.
* Certificate, HSM, mTLS and firmware management are outside this repository's code. Nothing
  in the chain code issues, checks or revokes a device certificate or firmware hash.
