# Bug Bounty Program

## Scope
Our bug bounty program covers the NHBChain core node implementation, official SDKs, and reference wallets. Third-party integrations, experimental branches, and deprecated releases are out of scope unless explicitly stated otherwise. Review the [audit readiness pack](../../ops/audit-pack/README.md) for component boundaries and configuration details when planning your research.

## Rewards
We offer tiered rewards based on the severity and impact of the vulnerability:

| Severity | Example Impact | Reward (USD) |
| --- | --- | --- |
| Critical | Remote code execution, consensus failure | $10,000 – $25,000 |
| High | Privilege escalation, theft of locked funds | $4,000 – $10,000 |
| Medium | Cross-tenant data leakage, double-spend vectors | $1,000 – $4,000 |
| Low | Denial-of-service, information disclosure | $250 – $1,000 |
| Informational | Hardening opportunities | Swag & public recognition |

Rewards are paid in USDC or BTC at the researcher’s discretion within 10 business days of triage confirmation. Bonus multipliers may be applied for high-quality reports with working proofs-of-concept.

## Service Level Agreements
* **Acknowledgement:** We confirm receipt of submissions within **24 hours**.
* **Triage:** We complete an initial severity assessment within **5 business days**.
* **Fix Commitment:** We communicate remediation plans within **10 business days** of triage.
* **Reward & Disclosure:** We target patch release and reward payment within **30 days** for high and critical issues, and **45 days** for other severities.

SLA targets may be adjusted when coordinated disclosure with upstream dependencies is required. Reporters are kept informed of any extensions.

## Embargo Policy
Researchers are expected to keep vulnerability details confidential until a coordinated disclosure date is agreed upon. We request a minimum 45-day embargo for critical issues and 30 days for other severities, unless a fix is released earlier. Premature disclosure may impact reward eligibility.

## Reporting & Contacts
Send encrypted reports to **security@nhbchain.io** with the subject line “Bug Bounty Submission.” Include detailed reproduction steps, affected components, and suggested remediation if available. Urgent matters can also be escalated via our Signal hotline `+1-415-555-0119` (voice/text only).

## PGP Key
* **Fingerprint:** `8F2D 3C71 9A0B 4D52 8EFA 9C1B 6D74 C5A2 1D3F 8B9E`
* **Key:** See [`docs/security/repository-pgp-key.asc`](./repository-pgp-key.asc).

