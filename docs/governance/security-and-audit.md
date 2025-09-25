# Governance Security and Audit Notes

> Include function-level documentation for developer integrations and technical specs; docs must be generated into /docs/governance/* for auditors, investors, regulators, and consumers.

## Snapshot Integrity

Governance voting power references the POTSO composite weight snapshot from the
previous epoch (`E-1`). The epoch module persists each snapshot under
`snapshots/potso/<epoch>/weights`, allowing auditors to verify that governance
ballots reflect the exact leaderboard finalised before the voting window
opened. Because the snapshot is taken prior to voting, sudden stake movements or
sybil addresses created after epoch finalisation cannot influence the weight
used for ballots. Validators should monitor snapshot retention and integrity as
part of routine state audits to ensure voting power remains tamper-evident.

## Timelock Review

Passed proposals must be explicitly queued before they can execute. Once
queued, the governance engine enforces the configured timelock by refusing to
apply the payload until `now >= TimelockEnd`. Operators should monitor for
`gov.queued` events to confirm that a passed proposal has entered the timelock
queue, and alert if an execution attempt occurs before the unlock timestamp
(`gov.executed` will not be emitted in that case). This ensures downstream
systems have a deterministic grace period to audit the queued change.

Execution is idempotent: after a proposal is applied the engine transitions it
to `executed` status and future calls are rejected. Auditors can therefore rely
on `gov.executed` as a single-source-of-truth signal that the param store
modifications were committed exactly once. Attempted replays or duplicate
messages will fail with an explicit error, preserving change-control logs and
reducing the risk of multi-apply bugs.

## Tally Reproducibility

Auditors can independently recompute vote tallies by iterating the
`gov/vote-index/<proposal>` bucket. Each entry contains the voter address,
choice, and voting power in basis points. Summing the weights per choice and
deriving `yes_ratio_bps = yes / (yes + no)` (abstain excluded) should reproduce
the `gov.finalized` event attributes. Turnout is the aggregate voting power
across yes, no, and abstain selections. Verifying the tally against the stored
snapshot ensures the governance engine did not mis-apply quorum or threshold
logic when finalising a proposal.
