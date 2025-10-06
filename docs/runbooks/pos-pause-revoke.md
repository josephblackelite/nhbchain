# POS Pause & Revoke Runbook

Use this runbook to pause merchant sponsorship or revoke compromised POS devices without interrupting raw transfers. The runtime checks the POS registry before quota enforcement, so registry updates take effect immediately for sponsored requests while the sender-funded path remains available.【F:core/sponsorship.go†L231-L247】 The administrative surface mirrors the onboarding API and exposes explicit RPCs for pausing or restoring merchants and devices.【F:proto/pos/registry.proto†L37-L82】

## Pause or resume a merchant

1. Submit `pos.v1.MsgPauseMerchant` (or `MsgResumeMerchant`) through governance. The registry creates the merchant record on demand, flips the `Paused` flag, and persists the change idempotently.【F:native/pos/registry.go†L101-L133】
2. Confirm the pause landed by querying the merchant record via the state manager. The helper normalises the address the same way the runtime does, so a paused merchant always round-trips with `Paused=true`.【F:core/state/pos_registry.go†L22-L67】
3. Broadcast a sponsored transaction referencing the merchant and verify that `EvaluateSponsorship` returns `Status=throttled` with a pause reason while the unsigned path still executes.【F:core/tx/checks.go†L9-L45】【F:core/sponsorship_test.go†L203-L270】

## Revoke or restore a device

1. Submit `pos.v1.MsgRevokeDevice` (or `MsgRestoreDevice`) for the affected terminal. The registry enforces deterministic normalisation, preserves the merchant association, and toggles the `Revoked` flag in place.【F:native/pos/registry.go†L155-L220】
2. Read the device snapshot via the state helper to verify the revocation status and associated merchant. Missing devices return `(nil, false)` so operators can detect stale identifiers before rolling back changes.【F:core/state/pos_registry.go†L69-L106】
3. Re-run the sponsored transaction from the device to ensure the runtime rejects the paymaster path with a revocation reason while allowing unsigned transfers to succeed.【F:core/tx/checks.go†L27-L45】【F:core/sponsorship_test.go†L273-L330】

## Clean up unused records

If a merchant or device has been decommissioned, remove the registry entry via governance to keep the dataset tidy. The manager helpers expose `POSDeleteMerchant` and `POSDeleteDevice`, which normalise identifiers before deleting the underlying key.【F:core/state/pos_registry.go†L57-L116】
