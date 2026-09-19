# POS Merchant & Device Onboarding Runbook

The POS registry decides which merchants and devices may have their transactions sponsored
by a paymaster. It holds one small record per merchant and per device. This runbook lists
what the registry stores, what it changes on chain, and what the code currently does with
registry transactions. Sources: `native/pos/registry.go`, `proto/pos/registry.proto`,
`core/state/pos_registry.go`, `core/state_pos.go`, `core/tx/checks.go`,
`consensus/codec/codec.go`.

## What the registry stores

| Record | State key | Fields |
| --- | --- | --- |
| Merchant | `pos/merchant/<address>` | `Address`, `Paused`, `Nonce`, `ExpiresAt`, `ChainID` |
| Device | `pos/device/<device id>` | `DeviceID`, `Merchant`, `Revoked`, `Nonce`, `ExpiresAt`, `ChainID` |
| Signer nonce | `pos/signer/<authority>` | last `Nonce` used by that authority |

* The merchant address is trimmed and lowercased. It is not checked to be a valid bech32
  address. The device ID is trimmed only.
* A merchant record is created with `Paused = false`. Registering a merchant that already
  exists keeps its `Paused` value. Registering a device that already exists rebinds it to
  the new merchant and keeps its `Revoked` value.
* Every registry call needs a nonce greater than zero and greater than the last nonce that the
  same authority used (`pos: stale nonce <n> (last <m>)`). `ExpiresAt` and `ChainID` are stored
  on the record. The registry code does not validate either of them.
* The registry does not store tax identifiers, sponsorship limits or firmware data.
  Sponsorship limits are the global settings in [paymaster budgets](./paymaster-budgets.md).

## What the registry changes

The registry is consulted only when a transaction requests sponsorship
(`CheckPOSRegistry` in `core/tx/checks.go`). A raw, unsponsored transfer never reads it.

* Merchant with `Paused = true`: sponsorship is `throttled` with `merchant sponsorship paused`.
* Device with `Revoked = true`: `device sponsorship revoked`.
* Device bound to a different merchant than the transaction's `merchantAddr`:
  `device registered to merchant <address>`.
* A merchant or device with no record is not blocked by this check.

`tx_previewSponsorship` shows the outcome without submitting anything.

## Messages

`pos.v1.Registry` (`proto/pos/registry.proto`) defines `RegisterMerchant`, `RegisterDevice`,
`PauseMerchant`, `ResumeMerchant`, `RevokeDevice` and `RestoreDevice`. Each message has
`authority`, `merchant_addr`, a nonce, `expires_at` and `chain_id` (device messages also have
`device_id`). The message set has no caps, firmware or certificate messages.

On chain, registry updates are transactions of type `TxTypePOSRegistry` (0x23) and are applied
by `applyPOSRegistry` in `core/state_pos.go`. The authority is the account that signed the
transaction, taken from the signature. The code performs no role or allow-list check on it, and
the `authority` field inside the message is not used. The gRPC `RegisterMerchant` handler in
`rpc/pos_grpc.go` goes through `submitPayload`, which signs only when the server was built with a signer and
otherwise submits an empty signature (the comment on `submitPayload`, `rpc/pos_grpc.go`, explains
why it is left that way), so it cannot produce a client-authorised transaction; a client has to
sign and submit the transaction itself.

## Current behaviour of `applyPOSRegistry`

Read this before relying on any registry operation. `applyPOSRegistry` decodes the payload
into `MsgRegisterMerchant` first and takes that branch whenever the payload's field 2
(`merchant_addr`) is non-empty. All six registry messages carry `merchant_addr` as field 2, and
decoding a pause, resume, device-registration, revoke or restore message into
`MsgRegisterMerchant` succeeds, so those messages also enter that branch. The branch calls
`Registry.RegisterDevice` with the device ID `unknown` and then `Registry.UpsertMerchant`, both
with the same authority and nonce. The first call records the nonce, so the second fails with
`pos: stale nonce <n> (last <n>)` and the transaction returns that error. The branches for
`MsgRegisterDevice` and `MsgPauseMerchant` come after it, and there is no branch at all for
`MsgResumeMerchant`, `MsgRevokeDevice` or `MsgRestoreDevice`.

The registry methods themselves (`UpsertMerchant`, `PauseMerchant`, `ResumeMerchant`,
`RegisterDevice`, `RevokeDevice`, `RestoreDevice` in `native/pos/registry.go`) behave as
described above when called directly, but this transaction path does not reach them.
`consensus/codec` also wraps a registry message from a signed envelope into the transaction
`Data` field as a serialised `google.protobuf.Any`, not as the bare message.

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
