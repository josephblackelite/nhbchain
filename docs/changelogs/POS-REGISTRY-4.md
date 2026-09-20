# POS-REGISTRY-4 — Merchant/Device Registry & Pause Controls

## Summary
- Added a POS registry module that normalises merchant/device identifiers, supports idempotent onboarding, and toggles pause or revocation flags in place.
- Exposed state-manager helpers, sponsorship gating, and regression tests so paused merchants or revoked devices block sponsored transactions without affecting raw transfers.
- Published `pos.v1.Registry` protobuf definitions (`proto/pos/registry.proto`) plus runbooks covering onboarding, pause, and revoke workflows. The `Registry` gRPC service itself is retired and not registered on the node (`rpc/http.go`, NHB-AUDIT-S3); registry mutations are carried by `TxTypePOSRegistry` (0x23) transactions (`core/state_pos.go` `applyPOSRegistry`), which currently apply only `MsgRegisterMerchant`, `MsgRegisterDevice` and `MsgPauseMerchant`. `MsgResumeMerchant`, `MsgRevokeDevice` and `MsgRestoreDevice` are accepted but change nothing.

## Operator Actions
- The onboarding and pause/revoke runbooks describe the intended registry workflows; note the limits above (no gRPC endpoint, and resume/revoke/restore are not applied by the transaction handler) before relying on them operationally.
