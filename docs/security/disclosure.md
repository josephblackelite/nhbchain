# Responsible Disclosure Policy

We appreciate security researchers who help us keep the NHBChain ecosystem safe. This policy describes how to report vulnerabilities and what you can expect from our team.

## Scope
The policy applies to:
- NHBChain core node software and the native modules shipped in this repository (`core/`, `native/`, `consensus/`, `rpc/`, `p2p/`).
- The command-line utilities (`cmd/`) and SDKs (`sdk/`) in this repository.
- Production infrastructure operated by NHBChain Labs (validators, API gateways, and hosted explorers).

The following assets are **out of scope**: third-party wallets, forked chains, legacy releases older than nine months, and experimental feature branches. If you are unsure whether a target is in scope, contact us before testing.

## Reporting Process
1. Gather detailed reproduction steps, logs, and proof-of-concept material.
2. Encrypt the report to the [repository PGP key](./repository-pgp-key.asc) (fingerprint `8C12 7674 689A AB92 A4DE  4643 E847 50CA 0E2F 4459`; confirm the fingerprint of the key you import matches), then email it to `security@nhbcoin.com`.
3. For time-sensitive issues, call or text the Signal hotline `+13234559568` after sending the report.
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
- **Primary Contact:** `security@nhbcoin.com`
- **Emergency Contact:** Signal `+13234559568`
- **PGP Key:** [`repository-pgp-key.asc`](./repository-pgp-key.asc), RSA 4096, UID `NHBCoin Security Team <security@nhbcoin.com>`, fingerprint `8C12 7674 689A AB92 A4DE  4643 E847 50CA 0E2F 4459`. The same address and key URL are published in `.well-known/security.txt`.

