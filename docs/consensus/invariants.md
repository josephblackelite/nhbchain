# Consensus Invariants

This document records the safety and liveness invariants that consensus engineers and auditors must uphold when modifying nhbchain's core protocols.

## State machine invariants

1. **Deterministic execution.** Given the same block inputs and prior state, every validator must produce the same resulting state root. Side-effecting I/O or non-deterministic randomness is prohibited in state transitions.
2. **Monotonic heights.** Block heights increase by exactly one with each committed block. Any deviation signals a fork or database corruption.
3. **Validator set consistency.** Changes to the validator set must be applied at the end of a block and take effect on the subsequent height, ensuring all nodes agree on the active set.
4. **Total stake conservation.** Slashing, staking, and rewards must balance so that total recorded stake equals previous stake plus net issuance minus penalties.
5. **Fee accounting.** Gas and transaction fees must be deducted exactly once and routed to the configured sink address.

## Networking invariants

- **Quorum availability.** At least `2f + 1` voting power must participate in pre-commit for a block to finalize. Monitor for persistent drops below quorum.
- **Peer scoring.** Misbehaving peers should be demoted or banned without affecting well-behaved connections.
- **Evidence propagation.** Equivocation and light-client attack evidence must be gossiped across the network within the unbonding window.

## Upgrade invariants

- **Genesis compatibility.** Upgrades must transform state in a single block and preserve existing account balances and governance records.
- **Parameter migrations.** Changes to parameter schemas require default values for legacy fields and migration routines that backfill missing data.
- **Replay protection.** Nodes that replay blocks through an upgrade must produce identical app hashes to freshly synced nodes.

## Validation checklist

- Add unit tests asserting invariant preservation for new state-transition code.
- Run the integration test suite (`make test-integration`) after protocol changes.
- Capture before/after snapshots of validator sets and stake totals for manual verification.
- Document any temporary deviations (e.g., emergency governance actions) in the upgrade notes with rollback instructions.
