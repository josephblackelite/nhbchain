# Arbitration Governance Guide

The arbitration program operates under the same transparent, proposal-driven
controls as the broader governance system. This guide explains how policy
changes reach the `ROLE_ARBITRATOR` allowlist, how frozen arbitrator policies are
captured inside escrow records, and which artifacts oversight teams should
collect when evaluating dispute outcomes.

## Managing the `ROLE_ARBITRATOR` Allowlist

Governance proposals that add or remove arbitrators use the `role.allowlist`
proposal kind. Payloads must target the `ROLE_ARBITRATOR` identifier and include
three components:

1. **Action list** – A deterministic list of addresses to grant or revoke.
2. **Realm scope** – The arbitration realm (for example, `core`) that the
   allowlist applies to. Governance rejects payloads that omit the realm or
   attempt to modify multiple realms in a single proposal.
3. **Audit memo** – A human-readable justification describing why each change is
   necessary and how conflicts of interest were evaluated. This memo is hashed
   into the proposal metadata so downstream reviewers can confirm it has not
   been altered.

When the proposal enters the voting period, the payload hash is logged in
`gov.proposed` and the targeted realm is emitted as an attribute. Observers can
replay the proposal by querying the governance archive or the RPC gateway to
confirm the address list matches the published memorandum.

Upon execution, the runtime updates the allowlist and appends an immutable
`gov.executed` audit entry. Arbitrator credentials take effect immediately after
execution; no manual key distribution occurs outside the on-chain state
transition. Rollback proposals follow the same structure so that removals are
verifiable and contestable.

## Frozen Policies Within Escrows

Each escrow is bound to the arbitration realm that existed at creation time. The
`escrow_getSnapshot` helper returns the frozen realm policy, including the
`realmVersion`, `policyNonce`, committee threshold, and exact arbitrator roster
captured when the escrow was opened. Because the frozen policy travels with the
escrow record and is re-surfaced in `escrow.*` events, arbitrators and
integrators can demonstrate that a dispute was evaluated under the rules that
were active when the contract was formed. Even if governance updates the realm
policy later, existing escrows continue to reference their embedded policy to
prevent retroactive changes.

The frozen policy metadata is also embedded in `escrow_listEvents` payloads
(`escrow.realm.*`, `escrow.dispute.*`, `escrow.resolved`). Indexers should
persist these attributes so external dashboards, regulators, and auditors can
reconstruct which rule set governed the resolution without performing additional
state reads.

## Expected Audit Artifacts

Regulators, investors, and third-party auditors should expect the following
artifacts when reviewing arbitration operations:

- **Proposal packet** – Original payload JSON, memo hash, and the governance
  archive entries (`gov.proposed`, `gov.finalized`, `gov.executed`).
- **Allowlist diff** – A before/after comparison of the arbitrator roster using
  `escrow_getRealm` responses for the relevant realm. The diff should include the
  sequence number and `updatedAt` timestamp emitted when governance executed the
  change.
- **Escrow evidence bundle** – Snapshot exports (via `escrow_getSnapshot`) for
  each disputed contract, including frozen policy metadata, dispute memos, and
  resolution payload hashes.
- **Event transcript** – Ordered `escrow_listEvents` output covering the dispute
  lifecycle, resolution decision, and signer fingerprints. This transcript lets
  auditors confirm that only allowlisted arbitrators signed the outcome.
- **Quarterly disclosures** – Aggregated statistics covering dispute volume,
  average time-to-resolution, and any escalations to governance for role updates
  or policy amendments.

## Reporting Cadence and Oversight Hooks

Arbitration reporting layers on top of the governance cadence:

- **Monthly dispute digest** – Published within seven days of month end. Includes
  dispute counts, outcome ratios (release/refund), average resolution time, and
  links to representative escrow snapshots. The digest must cite `escrow_getRealm`
  (realm version) and `escrow_listEvents` (decision sequence numbers) to prove
  figures can be independently verified.
- **Immediate disclosures** – Any revocation or suspension of arbitrator access
  requires same-day notice via a governance proposal update and a bulletin to the
  regulatory mailing list. The bulletin should link to the proposal ID and the
  allowlist diff derived from `escrow_getRealm`.
- **Oversight subscriptions** – Regulators and investors are encouraged to
  subscribe to `escrow.realm.updated` and `escrow.resolved` streams using
  `escrow_listEvents`. These hooks surface role changes and dispute outcomes in
  near real time, enabling watchdogs to cross-check that resolutions are signed
  by authorized arbitrators.
- **Annual controls review** – Once per fiscal year, governance sponsors a
  walkthrough of arbitration controls, including a sampling of frozen policy
  bundles, event transcripts, and reconciliation of dispute metrics against the
  published digests.

By anchoring each report and oversight hook to the canonical RPC endpoints
(`escrow_getRealm`, `escrow_listEvents`, and `escrow_getSnapshot`), stakeholders
can verify that arbitration remains governed by transparent, deterministic
processes aligned with the broader protocol lifecycle.
