# Audit Overview

This guide outlines how to plan and execute the multi-phase audit program for the nhbchain stack. Teams should use it as the entry point when preparing an engagement or triaging findings.

## Objectives

- Establish the scope of services, smart contracts, infrastructure, and operational processes under review.
- Define audit phases, owners, timelines, and the expected artifacts for each deliverable.
- Capture success criteria and exit gates so the team knows when the audit is considered complete.

## Phase sequencing

1. **Reconnaissance & documentation review.** Inventory system diagrams, data flows, third-party dependencies, and production infrastructure notes. Confirm that operators can supply configuration snapshots and chain state exports on request.
2. **Static analysis.** Run automated code scanning across the repository, tracking suppressions and interpreting rule coverage gaps.
3. **Fuzzing.** Stress consensus-critical and financial components to surface panics, invariant violations, and state divergence.
4. **End-to-end flows.** Exercise integration paths (wallets, gateways, bridge jobs) against realistic environments to detect regressions that automation may miss.
5. **Documentation quality.** Ensure runbooks and READMEs enable operators and auditors to reproduce findings and respond to incidents.
6. **Remediation & sign-off.** Track fixes through issue management, confirm regression tests exist, and capture the closing statement for leadership.

## Roles & responsibilities

| Role | Responsibilities |
| --- | --- |
| Audit coordinator | Maintains the schedule, collects artifacts, and facilitates sign-off. |
| Security engineer | Leads technical review of code, infrastructure, and operational configurations. |
| Service owner | Provides subject matter expertise, performs fixes, and validates remediation tests. |
| Compliance | Confirms controls alignment, ensures retention policies are respected, and signs off on procedural coverage. |

## Artifact checklist

- Scoped checklist describing in-scope repositories, services, and chains.
- Static-analysis findings report with issue owners and statuses.
- Fuzzer crash triage spreadsheet with reproduction steps and mitigations.
- E2E test transcripts (logs, traces, metrics dashboards) for each major workflow.
- Documentation gaps list referencing the relevant runbooks or READMEs.
- Final audit summary with outstanding risks, compensating controls, and follow-up actions.

## Using this directory

Each phase has its own guide under `docs/audit/`. Start with this overview, then work through the specific playbooks as you execute the engagement.
