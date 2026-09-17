# POS-REGISTRY-4 — Merchant/Device Registry & Pause Controls

## Summary
- Added a POS registry module that normalises merchant/device identifiers, supports idempotent onboarding, and toggles pause or revocation flags in place.【F:native/pos/registry.go†L15-L220】
- Exposed state-manager helpers, sponsorship gating, and regression tests so paused merchants or revoked devices block sponsored transactions without affecting raw transfers.【F:core/state/pos_registry.go†L22-L116】【F:core/tx/checks.go†L9-L45】【F:core/sponsorship.go†L231-L247】【F:core/sponsorship_test.go†L203-L330】
- Published `pos.v1.Registry` gRPC definitions plus runbooks covering onboarding, pause, and revoke workflows for operators.【F:proto/pos/registry.proto†L7-L82】【F:docs/runbooks/pos-onboarding.md†L1-L17】【F:docs/runbooks/pos-pause-revoke.md†L1-L19】

## Operator Actions
- Follow the onboarding and pause/revoke runbooks to register merchants, bind devices, and execute emergency controls as required.【F:docs/runbooks/pos-onboarding.md†L5-L17】【F:docs/runbooks/pos-pause-revoke.md†L5-L19】
