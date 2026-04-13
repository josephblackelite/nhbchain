# POTSO Consensus Integration

The BFT engine now consumes the composite weights that POTSO exposes for
validators. This document details how those weights feed into proposer
selection and quorum checks so operators can reason about liveness under mixed
stake and engagement scores.

## Weight source

Each consensus node implements `NodeInterface.GetValidatorSet()` which returns a
map keyed by validator address. The value is the validator's deterministic
weight derived from the sum of bonded stake and the current POTSO engagement
score. The engine caches this map at the beginning of every round and
recomputes the **total voting power** by summing all entries. Nodes must also
expose `NodeInterface.GetHeight()` so the engine can synchronise its internal
height with the committed chain height during restarts or catch-up scenarios.

If a validator appears in the set with a nil weight the engine treats its power
as zero. Validators missing from the set are ignored entirely.

## Round initialisation

`startNewRound` refreshes the validator set, recalculates the total voting
power, and zeroes per-vote accumulators before any proposal processing begins.
This guarantees that recycled votes from prior rounds cannot contribute toward
quorums in the new round and that weight changes are respected immediately.

## Vote accumulation

The engine tracks votes by type (prevote and precommit) and also maintains a
parallel map of accumulated power per type. When `addVoteIfRelevant` accepts a
vote it looks up the validator's weight and adds it to the appropriate
accumulator. Duplicate votes from the same validator are ignored so a single
validator cannot contribute more than its declared weight.

## Quorum thresholds

Threshold checks now compare the accumulated weight against a two-thirds power
threshold:

```
threshold = floor((2 * totalVotingPower) / 3)
reached   = accumulatedPower > threshold
```

The strict comparison (`>`, not `>=`) ensures the engine only advances once
more than two thirds of the known voting power backs the vote type. The commit
step reuses the precommit accumulator and does not recalculate totals again.

If the total voting power is zero or undefined the engine will never cross the
threshold, preventing empty validator sets from producing commits.

## Proposer selection

Proposers are still selected deterministically but now weight each validator by
`stake + engagement`. The selection seed uses the last commit hash and round
number, then consumes the combined weights as buckets when picking the winner.
If all weights are zero the engine falls back to round-robin across validators.

## Operational guidance

- **Monitoring:** Operators should alert on scenarios where accumulated power
  stalls below the two-thirds threshold despite a majority of validators
  signing, as this likely indicates stale or mismatched POTSO engagement data.
- **Weight changes:** Because the engine refreshes weights every round, any
  update propagated through `GetValidatorSet()` immediately impacts proposer
  probability and quorum calculations at the next round transition.
- **Testing:** Weighted quorum behaviour is covered by `consensus/bft/bft_test`
  which now includes scenarios exercising uneven stake and engagement mixes.

By aligning proposer selection and quorum formation with POTSO-derived weights,
the network maintains deterministic safety guarantees while reflecting
real-world validator performance metrics in consensus outcomes.
