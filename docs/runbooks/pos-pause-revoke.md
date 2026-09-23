# POS Pause & Revoke Runbook

Use this runbook to pause merchant sponsorship or revoke compromised POS devices without
interrupting raw transfers. The sponsorship check reads the POS registry before it applies the
paymaster caps, so a registry update takes effect for the next sponsored request while the
sender-funded path remains available (`EvaluateSponsorship` in `core/sponsorship.go`).

> **Status note (verified against the code):** the `pos.v1.Registry` gRPC service is retired and
> not registered on the node (`rpc/http.go`, comment NHB-AUDIT-S3). The registry messages
> (`proto/pos/registry.proto`) are carried by `TxTypePOSRegistry` (`0x23`) transactions, and
> `applyPOSRegistry` (`core/state_pos.go`) applies all six of them. Who may send them, the
> nonce rule and the payload format are in [POS merchant and device onboarding](./pos-onboarding.md).

## Pause or resume a merchant

* `native/pos/registry.go` implements `PauseMerchant` and `ResumeMerchant`. Both create the
  merchant record when it is missing, and setting the same value again changes nothing except
  the stored nonce, expiry and chain ID.
* The messages are `pos.v1.MsgPauseMerchant` and `pos.v1.MsgResumeMerchant`, sent as a
  `TxTypePOSRegistry` transaction signed by the merchant owner or by a holder of
  `ROLE_POS_REGISTRY_ADMIN`. The `nonce` must be above the signer's last registry nonce
  (`pos: stale nonce` otherwise).
* A paused merchant is refused for sponsorship with the reason `merchant sponsorship paused`
  (`core/tx/checks.go`). Only sponsorship is affected; the merchant's own transactions are not
  blocked.
* Verify by reading `pos/merchant/<address>` (`Manager.POSGetMerchant`) and by running
  `tx_previewSponsorship` for a sponsored transaction that names the merchant: the result must
  show `throttled` with `merchant sponsorship paused` while the flag is set.

## Revoke or restore a device

* `RevokeDevice` and `RestoreDevice` require an existing device record (`pos: device not
  registered` otherwise), keep the merchant binding and only flip `Revoked`.
* The messages are `pos.v1.MsgRevokeDevice` and `pos.v1.MsgRestoreDevice`. The signer must own the
  merchant the device is bound to or hold `ROLE_POS_REGISTRY_ADMIN`; a `merchant_addr` in the
  message that differs from the bound merchant is refused (`requirePOSRegistryDeviceAuthority`,
  `core/state_pos.go`).
* Verify by reading `pos/device/<device id>` (`Manager.POSGetDevice`; a missing device returns
  "not found") and with `tx_previewSponsorship`, which must show `throttled` with
  `device sponsorship revoked`.

## Clean up records

`Manager.POSDeleteMerchant` and `Manager.POSDeleteDevice` exist in `core/state/pos_registry.go`.
No transaction type calls them.
