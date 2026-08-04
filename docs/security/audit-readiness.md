# Audit Readiness Guide

This guide equips independent assessors with the artifacts and processes required to audit NHBChain.

## Scope & Architecture Overview
Audits should prioritize the consensus engine, staking and escrow modules, RPC gateway, and validator configuration. The [ops/audit-pack](../../ops/audit-pack/README.md) contains architecture diagrams, frozen dependencies, and configuration references. External smart contracts deployed by partners are out of scope unless explicitly documented in the audit brief.

## Documentation & Artifacts
- **Frozen Commit Hash:** `ops/audit-pack/FROZEN_COMMIT.txt` currently records a hash that does not exist in this repository's git history. It must be regenerated from a real commit before it can be relied on to identify the revision to audit.
- **Reproducible Build:** `ops/audit-pack/BUILD_STEPS.md` walks through deterministic builds using Docker and Nix.
- **Configuration Samples:** Example validator, RPC, and wallet configuration lives under `ops/audit-pack/config-samples/`.
- **Seeds & Fixtures:** Deterministic chain seeds and integration fixtures are published under `ops/audit-pack/seeds-fixtures/` to help auditors replay consensus scenarios.
- **Release Notes:** Security-impacting release notes are in `docs/security/release-process.md`.

## Expectations & SLAs
- **Kickoff:** We schedule an onboarding call within 2 business days of receiving an audit request.
- **Response Time:** Clarifications and artifact requests receive answers within 1 business day during the engagement.
- **Patch Turnaround:** Critical findings receive hotfix builds within 7 days; high-severity issues within 14 days.

## Embargo & Disclosure
Audit findings are treated under the same embargo rules as vulnerability submissions: 45 days for critical issues and 30 days for others, unless early release is mutually agreed. Public disclosure will include auditor attribution when approved.

## Contacts & Encryption
- **Program Lead:** `audit@nehborly.net`
- **Security Team:** `security@nehborly.net`
- **PGP Key:** [`repository-pgp-key.asc`](./repository-pgp-key.asc) is currently a corrupted ASCII-armored block and cannot be imported or used to encrypt anything. It must be regenerated before relying on it; no working fingerprint is published until then.

