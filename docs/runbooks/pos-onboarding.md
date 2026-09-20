# POS Merchant & Device Onboarding Runbook

The POS registry lets operations teams bootstrap merchants and their payment devices before rolling out sponsored transactions. Merchant entries default to an active sponsorship state and devices inherit their merchant binding while preserving any prior revocation flags, so the onboarding flow is idempotent.【F:native/pos/registry.go†L77-L184】 The registry is exposed over the `pos.v1.Registry` gRPC surface so governance tooling can stage the mutations alongside other administrative actions.【F:proto/pos/registry.proto†L7-L82】

## Register a merchant

1. Submit a `pos.v1.MsgRegisterMerchant` as a `TxTypePOSRegistry` (0x23) transaction. `tx.Data` is the message packed in a `google.protobuf.Any`; the Any's type URL is what tells the registry which message it is. The transaction must be signed by the account that owns the merchant address, or by a key holding `ROLE_POS_REGISTRY_ADMIN`; the message's `authority` field, if set, must be the signer. The registry normalises the supplied address, creates the merchant record if it does not already exist, and leaves the sponsorship flag enabled.【F:native/pos/registry.go†L77-L133】
2. Confirm the record landed by reading the merchant snapshot with a state query. The manager helper returns `(nil, false)` when the merchant has not been onboarded yet, so any subsequent calls can reuse the same check.【F:core/state/pos_registry.go†L22-L55】

## Register a device

1. Submit `pos.v1.MsgRegisterDevice` the same way to bind the device identifier to the merchant. The signer must own the merchant or hold `ROLE_POS_REGISTRY_ADMIN`; a device that is already bound to another merchant can only be moved by that merchant's owner or an admin. Repeated invocations update the association deterministically and carry forward any existing revocation flag so that migrations cannot silently re-enable a blocked terminal.【F:native/pos/registry.go†L155-L204】
2. Inspect the device record through the state manager to confirm the binding and revocation state. The helpers apply the same normalisation as the runtime, so the read-back values match the data that gates sponsorship at execution time.【F:core/state/pos_registry.go†L69-L104】

## Validate sponsorship gating

After onboarding, broadcast a sponsored transfer and verify that the evaluation path observes the merchant/device metadata. The state processor loads the registry before enforcing quota caps, so any pause or revocation applied later immediately suppresses sponsorship while leaving raw transfers untouched.【F:core/sponsorship.go†L231-L247】 Use the regression tests as a reference if you need to reproduce the acceptance checks locally.【F:core/sponsorship_test.go†L203-L330】
