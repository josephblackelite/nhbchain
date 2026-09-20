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

## The round a height starts in

A height starts in round 1 at every validator, whichever way the height before it ended.
After the engine's own commit, `commit` leaves round 0 and `startNewRound` moves on one
round from it, as it does for an engine that has just been built; a height the node reaches
because it took a block from a peer (`syncHeightWithNodeLocked`, which sets round 0) is moved
to round 1 in the same way. Two validators that start a height in different rounds hear
nothing from each other until the round of the one behind times out.

`NotifyExternalCommit` ends the round in progress only when the node's chain has reached the
height of that round. A peer's block that reaches the node while the engine is committing the
same one is answered without an error and signalled all the same, and the engine has already
counted it: taken for news it ended the first round of the next height and started that height
one round ahead of the peer. See [Block cadence](block-cadence.md).

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
