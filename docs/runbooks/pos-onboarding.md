# POS Merchant & Device Onboarding Runbook

The POS registry lets operations teams bootstrap merchants and their payment devices before rolling out sponsored transactions. Merchant entries default to an active sponsorship state and devices inherit their merchant binding while preserving any prior revocation flags, so the onboarding flow is idempotent.【F:native/pos/registry.go†L77-L184】 The `pos.v1.Registry` message definitions live in `proto/pos/registry.proto`, but the gRPC service itself is retired (see the status note below).【F:proto/pos/registry.proto†L7-L82】

> **Status note (verified against the code):** the `pos.v1.Registry` gRPC service is retired and not registered on the node (`rpc/http.go` `Serve`, NHB-AUDIT-S3). The registry messages are carried by `TxTypePOSRegistry` (`0x23`) transactions, and `applyPOSRegistry` (`core/state_pos.go`) applies all six: `MsgRegisterMerchant`, `MsgPauseMerchant`, `MsgResumeMerchant`, `MsgRegisterDevice`, `MsgRevokeDevice` and `MsgRestoreDevice`; any other payload is refused with `pos: invalid registry payload`. Steps below that mention `pos.v1.Msg*` refer to those transaction payloads, not to a gRPC endpoint.

## Who may submit a registry transaction

The signer of the transaction is the authority (`applyPOSRegistry`, `core/state_pos.go`). If a message carries an `authority` field it must name the signer, otherwise the transaction is refused (`requirePOSRegistrySignerIsAuthority`). The signer must be the owner of the merchant address being changed (the merchant address, written as bech32 or `0x` hex, must be the signer's own address) or hold `ROLE_POS_REGISTRY_ADMIN` (`requirePOSRegistryAuthority`); otherwise the error is `pos: signer may not change this registry entry`.

* Registering a device needs authority over the merchant named in the message. If the device already exists, the merchant it is currently bound to must also agree, so one merchant cannot take over another's device.
* Revoking or restoring a device needs authority over the merchant the device is bound to (`requirePOSRegistryDeviceAuthority`); if the message names a merchant it must be that one, and an unregistered device is refused with `pos: device <id> not registered`.
* Every call needs a nonce greater than zero and greater than the last nonce used by the same signer (`pos: stale nonce <n> (last <m>)`, `native/pos/registry.go` `ensureFreshNonce`).

## Register a merchant

Submit a signed `TxTypePOSRegistry` transaction carrying `MsgRegisterMerchant`, signed by the
merchant owner or a `ROLE_POS_REGISTRY_ADMIN` holder. Confirm the merchant by reading the key `pos/merchant/<address>` with the state manager
(`Manager.POSGetMerchant`); it returns "not found" for an unregistered merchant. No RPC method
returns registry records.

## Register a device

Submit `MsgRegisterDevice` the same way and read
`pos/device/<device id>` (`Manager.POSGetDevice`) to confirm the merchant binding and the
`Revoked` flag.

## Remove a record

`Manager.POSDeleteMerchant` and `Manager.POSDeleteDevice` delete a record. No transaction type
calls them, so records cannot be removed through the chain.
