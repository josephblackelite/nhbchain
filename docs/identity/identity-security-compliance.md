# Identity Security, Privacy & Compliance Brief

This document summarizes the controls surrounding the NHBChain identity subsystem for review by security teams, regulators, and
investors.

## Roles & Authorization

| Role | Capabilities | Controls |
| --- | --- | --- |
| Alias Owner | Register alias, link/unlink addresses, set primary, update avatar, rename. | Must sign EIP-191 payloads tied to
  aliasId, nonce, and expiry. Signatures verified on-chain. |
| Governance | Manage reserved list, adjudicate disputes, freeze aliases in extreme abuse scenarios. | Multisig governance module
  with on-chain proposals and audit logs. |
| Gateway Operator | Verify emails, manage API keys, moderate avatars. | Access via HMAC-authenticated admin console with hardware
  MFA and audit trails. |
| Watchers / Indexers | Subscribe to events for analytics. | Read-only; no privileged actions. |

## Authentication Requirements

* All JSON-RPC mutating calls require secp256k1 signatures from the alias owner (`identity_*` methods).
* Gateway write APIs require API key + HMAC signature (`X-API-Key`, `X-API-Signature`, timestamp).
* Avatar uploads optionally include `X-Alias-Signature` (owner-signed nonce) to prove authorization when uploading from third-party
  services.

## PII Boundary & Data Minimization

* On-chain: only alias string, aliasId, owner, linked addresses, avatarRef timestamps.
* Off-chain (Gateway): salted email hashes, verification timestamps, consent flags, API key metadata, audit logs.
* No plaintext emails leave the gateway. Hashing uses per-environment salt rotated quarterly.
* Claimables reference aliasId or hashed email only; no raw PII.

## Privacy Program

* Data retention: email verification logs retained 18 months, purge requests processed within 30 days (subject to legal holds).
* Data subject rights (DSAR): request triggers lookup by email hash; removal severs alias binding and deletes verification logs.
* Transparency: Wallets display whether alias opted in to email lookup; gateway exposes `/privacy/export` (future) for user export.
* Incident response: 4-hour SLA to acknowledge, 24-hour to provide mitigation status.

## Abuse & Fraud Controls

* Alias squatting mitigated with reserved names, staking deposits, and dispute resolution process.
* Email verification throttled per IP + per email hash; verification codes expire in 10 minutes and are rate-limited.
* Avatars scanned for malware and explicit content; flagged avatars can be replaced by governance action.
* Claimables protected via expiry (default 7 days) and signature binding; replayed signatures rejected (`IDN-008`).
* Comprehensive audit logs: `identity.alias.*` and `identity.claimable.*` events preserved for 365 days in archival nodes.

## Regulatory & Investor Notes

* Identity subsystem does **not** mint tokens; claimables simply escrow existing funds and settle deterministically upon claim.
* Alias operations are deterministic state transitions recorded on-chain; event logs include block height, tx hash, aliasId for
  audit reproducibility.
* Gateway services operate off-chain but expose signed audit trails (HMAC logs) and idempotent request IDs for reconciliation.
* Mint authority, consensus, and escrow modules remain unchanged. Identity module can be upgraded via governance without chain
  halt.
* Replay protection and nonce-based signatures satisfy standard replay-prevention requirements under financial regulations.
* Logs and proofs support AML monitoring; alias metadata can be correlated with on-chain transfers through deterministic IDs.

## Compliance Checklist

| Requirement | Status | Notes |
| --- | --- | --- |
| GDPR / CCPA data rights | ✅ | DSAR process documented; hashed emails minimize exposure. |
| SOC 2 logging | ✅ | Audit logs stored immutably with tamper-proof retention. |
| PCI scope | ✅ (out-of-scope) | No payment card data processed. |
| FinCEN guidance | ✅ | Claimables function as escrow, not custody change; standard SAR triggers available via events. |
| KYC dependency | Optional | Alias system integrates with external KYC providers if required by jurisdiction. |

## Incident Response & Monitoring

* Alerts on repeated failed HMAC signatures, sudden spike in alias registrations, or avatar moderation failures.
* Governance has emergency proposal to freeze alias (prevents transfers but preserves data) in case of phishing campaigns.
* Backups: Gateway database replicated across regions with point-in-time recovery; salts stored in HSM-backed secrets manager.

For engineering detail, see [identity.md](./identity.md) and [identity-gateway.md](./identity-gateway.md).
