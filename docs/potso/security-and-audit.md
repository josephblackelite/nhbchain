# POTSO Weighting – Security & Audit Notes

This document summarizes determinism guarantees and the procedure auditors can
follow to reproduce leaderboard results.

## Determinism Guarantees

- **Pure integer arithmetic:** All computations use integer math (`math/big` or
  64-bit integers) with fixed denominators, eliminating platform-dependent
  rounding. The EMA uses a 1e9 fixed-point scale to apply decay.

- **Stable ordering:** Tie-breaks are purely address based (`addrLex`) or digest
  based (`addrHash` with SHA-256). Given identical snapshots and configuration,
  every node produces the same order.

- **State persistence:** Each epoch stores a `potso.StoredWeightSnapshot`
  containing the epoch number, total stake/engagement and the ordered entries.
  This snapshot is sufficient to re-run the pipeline and verify payouts.

- **Config exposure:** `potso_params` exposes the active `[potso.weights]`
  configuration, ensuring reviewers know which parameters were applied when a
  snapshot was produced.

## Reproduction Procedure

1. Retrieve the target epoch via `potso_leaderboard` (or access the raw snapshot
   from the state trie).
2. Fetch the configuration for that epoch with `potso_params` (or from archival
   governance records).
3. Reconstruct the weighting pipeline by calling
   `ComputeWeightSnapshot(epoch, entries, params)` using the stored stake and
   engagement values.
4. Multiply the resulting weights by the epoch budget to verify payouts.

Because snapshots persist the final engagement values, auditors do not need
access to raw per-epoch meters—only the stored state and the configuration.

## State Integrity

- Snapshots and meters are written only once per epoch; attempting to reprocess
  an epoch yields the same snapshot and leaves existing state untouched.
- The leaderboard RPC is read-only and does not expose internal iteration order
  beyond the deterministic ranking.
- Any node can recompute the leaderboard from scratch using the stored meter
  data to confirm that persisted snapshots were produced correctly.

