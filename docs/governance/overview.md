# Governance Lifecycle

> Include function-level documentation for developer integrations and technical specs; docs must be generated into /docs/governance/* for auditors, investors, regulators, and consumers.

## Proposal Intake

Community members draft proposals off-chain using the published template, then
submit them through the governance module with a deterministic payload hash and
targeted change set. The intake endpoint records the author address, the
referenced POTSO snapshot epoch, and embeds the payload hash into the
`gov.proposed` event. Intake rejects submissions that omit mandatory disclosures
or reference an epoch newer than `E-1`, ensuring all voters share the same
eligibility dataset.

### Supported Proposal Kinds

Governance now supports five deterministic proposal types:

* `param.update` – Standard parameter adjustments for allow-listed runtime keys.
* `param.emergency_override` – Emergency parameter changes that follow the
  same quorum and timelock rules but emit dedicated audit entries so regulators
  can trace the override justification.
* `policy.slashing` – Updates to the slashing engine: enablement flag, penalty
  basis points, enforcement window, and maximum slash budget.
* `role.allowlist` – Grants or revokes addresses from governance-managed role
  allow-lists (for example, compliance or security operators).
* `treasury.directive` – Moves pre-approved balances from an allow-listed
  treasury account to one or more recipients with optional memos.

Each proposal kind has schema validation baked into the execution path; payloads
that violate documented ranges or include unknown fields are rejected before
they can be queued.

## Deposit Escrow Collection

Each proposal must attach the configured `MinProposalDeposit`. Deposits are held
in module escrow until the proposal concludes. Passing or vetoed proposals return
the deposit to the proposer; proposals that fail quorum or are abandoned forfeit
a portion to the community treasury to offset review costs. Deposits exist solely
to deter spam— they are not interest-bearing instruments and confer no financial
rights.

## Voting Period

Proposals enter the voting period as soon as the minimum deposit clears and
remain open until the `VotingEnd` timestamp recorded on-chain. Validators and
delegators may submit a single ballot per address selecting **yes**, **no**, or
**abstain**. Subsequent submissions overwrite the prior choice so wallets can
surface a "change vote" workflow without additional signing steps.

Ballot weight is derived from the POTSO composite engagement leaderboard to
ensure aligned incentives across staking and usage. Each vote pulls the
participant's basis-point share from the snapshot finalised at POTSO epoch
`E-1` (the most recently processed epoch) so that last-minute stake churn cannot
artificially inflate voting power. Addresses without weight in that snapshot
are rejected, preventing zero-power spam while still allowing abstentions from
eligible voters.

## Timelock Enforcement

Once a proposal finalises with a passing outcome, the proposer (or any keeper)
must queue it for execution. Queuing records `execute_after = finalized_at +
TimelockDuration`. The runtime refuses to execute the payload before the
timestamp elapses and emits `gov.queued` for monitoring systems. During the
timelock window, stakeholders can review the payload hash, compare it against the
original proposal, and raise alerts if downstream integrations require manual
intervention.

## Execution and Archival

After the timelock expires, any address may call `ExecuteProposal`. The runtime
verifies the payload hash, applies the parameter changes atomically, and emits
`gov.executed`. Completed proposals are archived with their final state, vote
summary, execution transaction hash, and an immutable audit record that
captures the executor, timestamp, and a JSON summary of the effect. The audit
log is append-only and keyed by sequence number so regulators and downstream
integrations can reconstruct the entire lifecycle without relying on external
event streams. Historical records remain queryable indefinitely, providing an
immutable timeline for regulators, investors, and the community.
