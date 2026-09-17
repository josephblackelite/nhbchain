# BFT Height Synchronisation

The BFT engine now aligns its internal height with the node's committed chain
height whenever it boots or resets a round. This prevents validators from
replaying proposals for heights that have already been finalised and ensures the
next proposal always targets `chain height + 1`.

## Interface contract

Consensus nodes must implement `NodeInterface.GetHeight()` alongside the
existing methods. The return value represents the latest block height that has
been durably committed to the local chain. The engine consumes this method in
three places:

- **Engine construction:** `NewEngine` seeds `currentState.Height` to
  `node.GetHeight() + 1` so a restarted validator immediately targets the next
  block height.
- **Round transitions:** `startNewRound` prunes any stale `committedBlocks`
  entries and fast-forwards to `node.GetHeight() + 1` when the cached state falls
  behind the node.
- **Post-commit cleanup:** `commit` re-runs the synchronisation helper to clear
  out entries for heights that have been finalised on the node.

## Operational impact

- **Restarts:** Validators that restart after falling behind no longer need to
  manually advance their consensus height. As soon as the engine enters the next
  round it observes the node height and jumps ahead automatically.
- **State safety:** Stale `committedBlocks` entries are pruned on every
  synchronisation pass, preventing spurious short-circuiting of later rounds.
- **Testing:** `consensus/bft/bft_test.go` now seeds a non-zero node height to
  ensure proposals targeting the resynchronised height are accepted after a
  simulated restart.

This behaviour keeps consensus progress aligned with the canonical chain without
requiring additional coordination from operators.
