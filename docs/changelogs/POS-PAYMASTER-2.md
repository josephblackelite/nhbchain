# POS-PAYMASTER-2 — Caps & Abuse Guards

## Summary
- Added persistent paymaster counters scoped to merchant, device, and global budgets with daily rollovers.
- Introduced configuration options for merchant NHB caps, per-device transaction limits, and a network-wide sponsorship ceiling.
- Emit a `paymaster.throttled` event for each throttled attempt (`core/events/sponsorship.go`), report the throttle details in the `tx_previewSponsorship` result, and provide operational guidance in the paymaster runbook. No RPC method returns the counters themselves (`docs/ops/paymaster.md`).

## Operator Actions
- Review `docs/ops/paymaster.md` for budgeting examples and alerting practices.
- Set `[global.Paymaster]` caps in the node configuration and watch `paymaster.throttled` events for hot merchants/devices (the counters are not exposed over RPC).
