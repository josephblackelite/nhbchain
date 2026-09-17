# Governance Security and Audit Notes

> Include function-level documentation for developer integrations and technical specs; docs must be generated into /docs/governance/* for auditors, investors, regulators, and consumers.

## Snapshot Integrity and Immutability

Governance voting power references the POTSO composite weight snapshot from the
previous epoch (`E-1`). The epoch module persists each snapshot under
`snapshots/potso/<epoch>/weights`, allowing auditors to verify that governance
ballots reflect the exact leaderboard finalised before the voting window
opened. Because the snapshot is taken prior to voting, sudden stake movements or
sybil addresses created after epoch finalisation cannot influence the weight
used for ballots.

Each snapshot is written once and addressed by epoch number plus the block hash
at which the epoch closed. The governance service validates the block hash
against the canonical chain head before accepting the snapshot ID, preventing a
malicious proposer from supplying alternate data. Storage writes are additionally
covered by consensus state proofs; replaying a divergent snapshot would be
rejected as the Merkle root would not match the signed block header.

Operators should monitor snapshot retention and integrity as part of routine
state audits to ensure voting power remains tamper-evident. When reconstructing
a vote, auditors must reference the immutable epoch archive and cross-check the
snapshot commitment embedded in the `gov.proposed` event.

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

## Emergency Overrides

`param.emergency_override` proposals follow the exact same quorum, deposit, and
timelock requirements as standard parameter updates. The only difference is
auditing: when the override executes the runtime appends an audit record with
`{"kind":"param.emergency_override"}` and the affected keys so regulators can
distinguish routine adjustments from emergency responses. Operators should use
the `memo` and proposal metadata to document the reason for the override and the
planned rollback path.

## Immutable Audit Log

Every governance milestone—proposal creation, votes, finalization, queueing, and
execution—now writes an append-only record to the on-chain audit log. Each entry
captures the event type, proposal ID, timestamp, optional actor address, and a
JSON detail blob summarising the effect (e.g. updated parameters, granted roles,
treasury transfer memo). The log is keyed by a monotonically increasing
sequence number, allowing auditors to reconstruct the full history without
replaying RPC events. Emergency overrides and treasury directives emit detailed
records so internal control teams can reconcile approvals against downstream
ledger systems.

## Replay and Idempotency Controls

The governance router enforces a strict proposal state machine. Each proposal ID
advances linearly: `draft -> voting -> finalized -> queued -> executed`.
Requests that do not match the expected next state are rejected with a plain
error (for example `"governance: proposal %d not accepting votes"`) rather than
a dedicated error type.

Votes are submitted to the RPC as a plain `{id, from, choice}` JSON object and
authenticated only by the caller's bearer JWT — `CastVote` does not verify a
cryptographic signature over the vote, and there is no per-vote nonce or
`chain_id` binding today. This means a vote is not cryptographically bound to
the voter's private key; anyone holding a valid bearer token for the `from`
address's session can cast or observe votes on its behalf.

## Tally Reproducibility

Auditors can independently recompute vote tallies by iterating the
`gov/vote-index/<proposal>` bucket. Each entry contains the voter address,
choice, and voting power in basis points. Summing the weights per choice and
deriving the following quantities reproduces the `gov.finalized` event
attributes:

- `total_active = yes_weight + no_weight`
- `yes_ratio_bps = floor((yes_weight * 10_000) / total_active)`
- `turnout_ratio_bps = floor(((yes_weight + no_weight + abstain_weight) * 10_000) / total_snapshot_power)`

Where `total_snapshot_power` is the aggregate power recorded in the referenced
snapshot. Abstentions do not affect the approval threshold but do count toward
turnout calculations. Verifying these ratios against the stored snapshot ensures
the governance engine did not mis-apply quorum or threshold logic when
finalising a proposal.

## Event Log Map

Auditors can observe governance lifecycle milestones through the following
events:

| Event | Trigger | Key Attributes |
| --- | --- | --- |
| `gov.proposed` | Proposal created and deposit escrowed. | `id`, `proposer`, `kind`, `deposit`, `votingStart`, `votingEnd`, `timelockEnd` |
| `gov.vote` | Ballot accepted during voting window. | `id`, `voter`, `choice`, `powerBps` |
| `gov.finalized` | Voting window closed and tally computed. | `id`, `status`, `turnoutBps`, `quorumBps`, `yesPowerBps`, `noPowerBps`, `abstainPowerBps`, `yesRatioBps`, `passThresholdBps`, `totalBallots` |
| `gov.queued` | Proposal enqueued into timelock. | `id`, `timelockEnd` |
| `gov.executed` | Timelock satisfied and payload applied. | `id`, `status` |
