# Security Release & Freeze Process

This document defines the security, audit, and operational procedures required before and during the public testnet launch freeze. Follow it for coordinating triage, escalations, and roll-forward plans.

## Audit Intake

1. **Submission Channel** – All issues must arrive via `security@nhbchain.io` or the HackerOne program. Public issues are not accepted during freeze.
2. **Acknowledgement SLA** – Respond to researchers within 24 hours with a tracking ID and severity placeholder.
3. **Initial Assessment** – Security triage team rates severity using CVSS 3.1 and determines impacted modules.
4. **Issue Tracking** – Log each report in the private security board with fields: severity, affected versions, exploit prerequisites, mitigation status.
5. **Communication** – Share sanitized updates with launch leadership during daily stand-ups.

## Fix Windows

| Severity | Containment SLA | Fix SLA | Deployment |
| --- | --- | --- | --- |
| Critical | Immediate containment | 48 hours | Hotfix prior to freeze lift |
| High | 24 hours | 72 hours | Include in next scheduled build |
| Medium | 3 days | 7 days | Bundle with minor release |
| Low | Best effort | Best effort | Document in backlog |

If an issue risks funds or validator safety, initiate the freeze protocol immediately regardless of severity classification.

## Pre-Launch Checklist

* Validate state sync snapshots and signature bundles.
* Verify hardened configuration templates for validators, seeds, RPC, and faucet nodes.
* Confirm seed rotation and faucet abuse scripts are operational.
* Rehearse swap, escrow, identity, POTSO, and governance end-to-end scenarios.
* Ensure all public endpoints (RPC/REST/WS, faucet API, explorer) pass synthetic monitoring checks.
* Publish launch documentation: [testnet](../launch/testnet.md), [faucet](../launch/faucet.md), [explorer](../launch/explorer.md).

## Freeze Procedures

1. Announce the freeze window and commit hash in `#launch-control` and the status page.
2. Lock main branches by revoking push permissions and enabling required reviews.
3. Disable automatic deployments; require manual approval for infrastructure changes.
4. Snapshot validator set and RPC state; archive artifacts in the secure bucket.
5. Activate enhanced monitoring dashboards for consensus health and endpoint latency.
6. Schedule standby rotations for security, SRE, and developer relations teams.

## Incident Response

1. Confirm impact with the triage team and assign an incident commander.
2. Notify stakeholders via status page and `#launch-incidents` channel.
3. Apply mitigations (configuration changes, firewall rules, feature toggles) and document actions.
4. Escalate to external partners if shared infrastructure is affected.
5. Record timeline, root cause, and remediation steps in the incident log within 24 hours.

## Rollback & Rollforward

* **Rollback:** Use stored snapshots to restore consensus state. Validators replay from the last good height. Communicate expected downtime and provide verification hashes.
* **Rollforward:** Once fixes are validated, tag a new release, update artifacts, and coordinate redeployments. Monitor key metrics for at least 2 epochs before declaring stability.

## Validator Hardening

* Enforce Linux kernel LTS with automatic security patching.
* Enable firewall rules: allow inbound 26656/26657 from trusted CIDRs, restrict SSH to bastion hosts with MFA.
* Run validators as non-root users with dedicated systemd units and resource limits.
* Configure auditd and ship logs to the central SIEM.
* Rotate consensus keys using the provided HSM integration guide before entering freeze.

## Vulnerability Disclosure

* Email: `security@nhbchain.io`
* PGP: `https://security.nhbchain.io/pgp.txt`
* Expected timeline: acknowledgement in 24 hours, fix ETA shared within 5 business days.
* Safe harbor applies for good faith testing that avoids mainnet, personal data, and denial-of-service attacks.

Researchers must not publicly share vulnerabilities until coordinated disclosure timelines are agreed upon. All incidents discovered during the freeze are reviewed in the post-launch retrospective.
