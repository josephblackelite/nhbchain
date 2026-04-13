# POS-PAYMASTER-2 — Caps & Abuse Guards

## Summary
- Added persistent paymaster counters scoped to merchant, device, and global budgets with daily rollovers.
- Introduced configuration options for merchant NHB caps, per-device transaction limits, and a network-wide sponsorship ceiling.
- Surfaced throttling events and RPC visibility for operators and provided operational guidance in the paymaster runbook.

## Operator Actions
- Review `docs/ops/paymaster.md` for budgeting examples and alerting practices.
- Set `[global.paymaster]` caps in the node configuration and monitor counters for hot merchants/devices.
