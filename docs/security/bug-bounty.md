# Bug Bounty Program

## Scope
Our bug bounty program covers the NHBChain core node implementation, official SDKs, and reference wallets. Third-party integrations, experimental branches, and deprecated releases are out of scope unless explicitly stated otherwise. Review the [audit readiness pack](../../ops/audit-pack/README.md) for component boundaries and configuration details when planning your research.

## Rewards
We offer tiered rewards based on the severity and impact of the vulnerability. Reward amounts
below are target USD-equivalent values, not USD payments — see the payment note underneath the
table.

| Severity | Example Impact | Target Value (USD-equivalent) |
| --- | --- | --- |
| Critical | Remote code execution, consensus failure | $10,000 – $25,000 |
| High | Privilege escalation, theft of locked funds | $4,000 – $10,000 |
| Medium | Cross-tenant data leakage, double-spend vectors | $1,000 – $4,000 |
| Low | Denial-of-service, information disclosure | $250 – $1,000 |
| Informational | Hardening opportunities | Swag & public recognition |

Rewards are paid in NHB or ZNHB, at the researcher's choice of asset, valued at the prevailing
on-chain reference price at time of payment to approximate the USD-equivalent target above.
Payment is made within 10 business days of triage confirmation. Bonus multipliers may be applied
for high-quality reports with working proofs-of-concept.

## Service Level Agreements
* **Acknowledgement:** We confirm receipt of submissions within **24 hours**.
* **Triage:** We complete an initial severity assessment within **5 business days**.
* **Fix Commitment:** We communicate remediation plans within **10 business days** of triage.
* **Reward & Disclosure:** We target patch release and reward payment within **30 days** for high and critical issues, and **45 days** for other severities.

SLA targets may be adjusted when coordinated disclosure with upstream dependencies is required. Reporters are kept informed of any extensions.

## Embargo Policy
Researchers are expected to keep vulnerability details confidential until a coordinated disclosure date is agreed upon. We request a minimum 45-day embargo for critical issues and 30 days for other severities, unless a fix is released earlier. Premature disclosure may impact reward eligibility.

## Reporting & Contacts
Send encrypted reports to **security@nhbcoin.com** with the subject line “Bug Bounty Submission.” Include detailed reproduction steps, affected components, and suggested remediation if available. Urgent matters can also be escalated via our Signal hotline `+13234559568` (voice/text only).

## PGP Key
* **Key:** [`docs/security/repository-pgp-key.asc`](./repository-pgp-key.asc) — RSA 4096, UID `NHBCoin Security Team <security@nhbcoin.com>`.
* **Fingerprint:** `8C12 7674 689A AB92 A4DE  4643 E847 50CA 0E2F 4459`
* Generated 2026-09-03, no expiration set. Verify the fingerprint above matches what you import before trusting it for an encrypted report.

