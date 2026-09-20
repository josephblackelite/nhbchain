# POS Merchant & Device Onboarding Runbook

The POS registry lets operations teams bootstrap merchants and their payment devices before rolling out sponsored transactions. Merchant entries default to an active sponsorship state and devices inherit their merchant binding while preserving any prior revocation flags, so the onboarding flow is idempotent.【F:native/pos/registry.go†L77-L184】 The `pos.v1.Registry` message definitions live in `proto/pos/registry.proto`, but the gRPC service itself is retired (see the status note below).【F:proto/pos/registry.proto†L7-L82】

> **Status note (verified against the code):** the `pos.v1.Registry` gRPC service is retired and not registered on the node (`rpc/http.go` `Serve`, NHB-AUDIT-S3). The registry messages are carried by `TxTypePOSRegistry` (`0x23`) transactions, and `applyPOSRegistry` (`core/state_pos.go`) currently applies only `MsgRegisterMerchant`, `MsgRegisterDevice` and `MsgPauseMerchant`; `MsgResumeMerchant`, `MsgRevokeDevice` and `MsgRestoreDevice` are accepted but change nothing. Steps below that mention `pos.v1.Msg*` refer to those transaction payloads, not to a gRPC endpoint.

## Register a merchant

Submit a signed `TxTypePOSRegistry` transaction carrying `MsgRegisterMerchant` (subject to
the section above). Confirm the merchant by reading the key `pos/merchant/<address>` with the state manager
(`Manager.POSGetMerchant`); it returns "not found" for an unregistered merchant. No RPC method
returns registry records.

## Register a device

Submit `MsgRegisterDevice` the same way (subject to the section above) and read
`pos/device/<device id>` (`Manager.POSGetDevice`) to confirm the merchant binding and the
`Revoked` flag.

## Remove a record

`Manager.POSDeleteMerchant` and `Manager.POSDeleteDevice` delete a record. No transaction type
calls them, so records cannot be removed through the chain.
