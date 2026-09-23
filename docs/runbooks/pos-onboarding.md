# POS Merchant & Device Onboarding Runbook

The POS registry records which merchants and devices exist and whether sponsorship for them is
paused or revoked. Merchant entries default to an active state and a device keeps its
`Revoked` flag when it is registered again, so registering twice is safe
(`native/pos/registry.go`). The `pos.v1.Registry` message definitions live in
`proto/pos/registry.proto`.

> **Status note (verified against the code):** the `pos.v1.Registry` gRPC service is retired and
> not registered on the node (`rpc/http.go`, comment NHB-AUDIT-S3; the implementation in
> `rpc/pos_grpc.go` is left unregistered). Registry changes are `TxTypePOSRegistry` (`0x23`)
> transactions. `applyPOSRegistry` (`core/state_pos.go`) applies all six registry messages:
> `MsgRegisterMerchant`, `MsgPauseMerchant`, `MsgResumeMerchant`, `MsgRegisterDevice`,
> `MsgRevokeDevice` and `MsgRestoreDevice`. Steps below that mention `pos.v1.Msg*` refer to those
> transaction payloads, not to a gRPC endpoint.

## Who may change the registry

The account that signs the transaction is the authority (`applyPOSRegistry`). Two rules
apply to every registry message:

* The signer must own the merchant address the change is about (the merchant address in the
  message, read as bech32 `nhb1...` or `0x` hex, equals the signer), or hold the role
  `ROLE_POS_REGISTRY_ADMIN` (`RolePOSRegistryAdmin`, `core/state_pos.go`). A signer with
  neither gets `pos: signer may not change this registry entry`. A merchant given as a free-form
  label that is not an address can therefore only be changed by a role holder. Roles are
  granted in the `roles` object of the genesis file or by a governance `role.allowlist`
  proposal for a role listed in `Governance.AllowedRoles`. The `roles` object of the live
  network's `config/genesis.relaunch.json` lists only `MINTER_NHB`, and the sample
  `config.toml` does not list `ROLE_POS_REGISTRY_ADMIN` in `AllowedRoles`, so on that chain only
  merchant owners can change the registry until a role is added to that list and granted.
* The message's `authority` field must be empty or name the signer. Any other value is refused
  with `pos: invalid registry payload`. The field is not the source of authority; the signature is.

For a device change the merchant that counts is the merchant the device is bound to. Registering
a device that already exists moves it to the merchant in the message, so the signer needs
authority over both the new merchant and the merchant it is bound to now. Revoke and restore
need an existing device (`pos: device not registered` otherwise), and a `merchant_addr` in the
message, when given, must equal the bound merchant.

Each authority has one nonce counter, stored at `pos/signer/<signer address>`. A message's `nonce`
must be positive and greater than the last one that signer used, or the transaction fails with
`pos: stale nonce <n> (last <m>)`. The `nonce`, `expires_at` and `chain_id` of a message are
stored on the record; the registry does not check `expires_at` or `chain_id` itself.

## Register a merchant

Submit a signed `TxTypePOSRegistry` transaction carrying `MsgRegisterMerchant`. Its payload
(`tx.Data`) is the serialized `google.protobuf.Any` wrapping the message, which
`consensus/codec` builds from a `TxEnvelope` whose payload is that message. Confirm the merchant by
reading the key `pos/merchant/<address>` with the state manager (`Manager.POSGetMerchant`); it
returns "not found" for an unregistered merchant. No RPC method returns registry records.

Registering does not change an existing merchant's paused flag.

## Register a device

Submit `MsgRegisterDevice` the same way and read `pos/device/<device id>`
(`Manager.POSGetDevice`) to confirm the merchant binding and the `Revoked` flag.

Registration is not a precondition for sponsorship: the sponsorship check only acts on a
merchant record that is paused and a device record that is revoked or bound to another merchant
(see [paymaster budget](./paymaster-budgets.md)).

## Remove a record

`Manager.POSDeleteMerchant` and `Manager.POSDeleteDevice` delete a record. No transaction type
calls them, so records cannot be removed through the chain.
