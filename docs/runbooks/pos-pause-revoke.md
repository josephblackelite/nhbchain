# POS Pause & Revoke Runbook

Use this runbook to pause merchant sponsorship or revoke compromised POS devices without interrupting raw transfers. The runtime checks the POS registry before quota enforcement, so registry updates take effect immediately for sponsored requests while the sender-funded path remains available.【F:core/sponsorship.go†L231-L247】 The proto definitions in `proto/pos/registry.proto` describe pause/resume and revoke/restore messages, but the gRPC service is retired (see the status note below).【F:proto/pos/registry.proto†L37-L82】

> **Status note (verified against the code):** the `pos.v1.Registry` gRPC service is retired and not registered on the node (`rpc/http.go` `Serve`, NHB-AUDIT-S3). The registry messages are carried by `TxTypePOSRegistry` (`0x23`) transactions, and `applyPOSRegistry` (`core/state_pos.go`) applies all six: `MsgRegisterMerchant`, `MsgRegisterDevice`, `MsgPauseMerchant`, `MsgResumeMerchant`, `MsgRevokeDevice` and `MsgRestoreDevice`. Steps below that mention `pos.v1.Msg*` refer to those transaction payloads, not to a gRPC endpoint. Who may sign them is described in [POS merchant and device onboarding](./pos-onboarding.md).

## Pause or resume a merchant

* `native/pos/registry.go` implements `PauseMerchant` and `ResumeMerchant`. Both create the
  merchant record when it is missing, and setting the same value again changes nothing except
  the stored nonce, expiry and chain ID.
* The messages are `pos.v1.MsgPauseMerchant` and `pos.v1.MsgResumeMerchant`, sent as a
  `TxTypePOSRegistry` transaction. `applyPOSRegistry` routes them to `Registry.PauseMerchant`
  and `Registry.ResumeMerchant` (`core/state_pos.go`). The signer must be the merchant owner or
  hold `ROLE_POS_REGISTRY_ADMIN`.
* Verify by reading `pos/merchant/<address>` (`Manager.POSGetMerchant`) and by running
  `tx_previewSponsorship` for a sponsored transaction that names the merchant: the result must
  show `throttled` with `merchant sponsorship paused` while the flag is set.

## Revoke or restore a device

* `RevokeDevice` and `RestoreDevice` require an existing device record (`pos: device <id> not
  registered` otherwise), keep the merchant binding and only flip `Revoked`.
* The messages are `pos.v1.MsgRevokeDevice` and `pos.v1.MsgRestoreDevice`. `applyPOSRegistry`
  routes them to `Registry.RevokeDevice` and `Registry.RestoreDevice`. The signer must be the
  owner of the merchant the device is bound to, or hold `ROLE_POS_REGISTRY_ADMIN`
  (`requirePOSRegistryDeviceAuthority`, `core/state_pos.go`).
* Verify by reading `pos/device/<device id>` (`Manager.POSGetDevice`; a missing device returns
  "not found") and with `tx_previewSponsorship`, which must show `throttled` with
  `device sponsorship revoked`.

## Clean up records

`Manager.POSDeleteMerchant` and `Manager.POSDeleteDevice` exist in `core/state/pos_registry.go`.
No transaction type calls them.
