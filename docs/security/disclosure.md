# Responsible Disclosure Policy

We appreciate security researchers who help us keep the NHBChain ecosystem safe. This policy describes how to report vulnerabilities and what you can expect from our team.

## Scope
The policy applies to:
- NHBChain core node software and associated smart-contract modules shipped in this repository.
- Official command-line utilities and SDKs published under the `nhbchain` GitHub organization.
- Production infrastructure operated by NHBChain Labs (validators, API gateways, and hosted explorers).

The following assets are **out of scope**: third-party wallets, forked chains, legacy releases older than nine months, and experimental feature branches. If you are unsure whether a target is in scope, contact us before testing.

## Reporting Process
1. Gather detailed reproduction steps, logs, and proof-of-concept material.
2. Encrypt your report with our [repository PGP key](./repository-pgp-key.asc) and email it to `security@nhbcoin.net`.
3. For time-sensitive issues, call or text the Signal hotline `+1-415-555-0119` after sending the encrypted report.
4. Do not share vulnerability details publicly or with third parties until we finalize remediation and agree on a disclosure timeline.

## Service Level Agreements
- **Acknowledgement:** within 24 hours.
- **Initial Response & Triage:** within 5 business days.
- **Status Updates:** every 7 days until resolution.
- **Resolution Target:** 30 days for critical/high issues, 45 days for medium, and 60 days for low severity reports.

If coordinated disclosure with upstream partners is required, we will provide updated timelines and rationale to the reporter.

## Embargo & Disclosure
We prefer coordinated disclosure and request a minimum embargo of 30 days from acknowledgement, extended to 45 days for critical issues. Public advisories will credit researchers who comply with the embargo and supply actionable reproduction steps.

## Safe Harbor
Testing activities conducted under this policy are authorized, provided you:
- Make a good-faith effort to avoid privacy violations, service disruption, and data destruction.
- Cease testing once you have established the impact of a vulnerability.
- Comply with all applicable laws.

If legal action is initiated by a third party against you for activities conducted in compliance with this policy, notify us and we will make this authorization known.

## Contacts & Encryption
- **Primary Contact:** `security@nhbcoin.net`
- **Emergency Contact:** Signal `+1-415-555-0119`
- **PGP Fingerprint:** `8F2D 3C71 9A0B 4D52 8EFA 9C1B 6D74 C5A2 1D3F 8B9E`
- **PGP Key:** [`repository-pgp-key.asc`](./repository-pgp-key.asc)

