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
Requests that do not match the expected next state are rejected with a
`StateTransitionError`. Execution payloads are protected by a content hash that
is logged when the proposal is created; the executor re-validates the hash
before applying the change so mutated payloads cannot be replayed.

On-chain signatures are bound to a `nonce` and `chain_id` value. The `nonce`
increments per account, preventing off-chain copied votes from being accepted,
while the `chain_id` disallows replaying the same transaction on a forked or
test environment. These safeguards ensure that even if a validator observes a
vote transaction, it cannot re-submit the message without the signer's private
key.

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
| `gov.proposed` | Proposal created and deposit escrowed. | `proposal_id`, `snapshot_epoch`, `payload_hash`, `deposit_amount` |
| `gov.vote` | Ballot accepted during voting window. | `proposal_id`, `voter`, `choice`, `weight_bps` |
| `gov.finalized` | Voting window closed and tally computed. | `proposal_id`, `yes_ratio_bps`, `turnout_bps`, `outcome` |
| `gov.queued` | Proposal enqueued into timelock. | `proposal_id`, `execute_after`, `payload_hash` |
| `gov.executed` | Timelock satisfied and payload applied. | `proposal_id`, `executor`, `effect_hash` |
