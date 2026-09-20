# Block Cadence

How often a chain of validators commits a block when nothing is asked of it, what
decides that, the one setting that pins it (`[consensus] MinBlockInterval`), and the
quantities of the chain that are counted in blocks and so follow it.

## What sets the pace

The BFT engine has no block time. The round of the next height starts the moment
the last block commits (the engine's own commit, or a block the node took from a peer)
and nothing waits between one block and the next. An idle chain therefore commits at
the speed of a single height: the proposer builds and validates an empty block, the
validators exchange a proposal, a prevote and a precommit, and each commits. Between
two validators in step that takes a few tens of milliseconds, so a chain that stayed in
step would commit 20 to 30 blocks a second.

A chain does not stay in step, and the pace it keeps over minutes is decided by that,
not by the speed of a height. A round that does not commit is not cut short: it lasts
the whole commit timeout (4 s by default), and a height whose validators are not in the
same round goes through several of them. Heights are therefore of two kinds and nothing
between: a height that commits in its first round takes a tenth of a second at most, and
a height that does not takes four seconds or more. Measured on a running two-validator
chain, from the moment each height's first round started (almost every block empty):

| stretch of the chain | heights | blocks/s | heights done in 0.25 s | heights of 4 s or more | share of the time spent in those |
| --- | --- | --- | --- | --- | --- |
| engine without the round-bound changes, an evening | 26,875 | 0.83 | 90.8% | 9.2% | 97.1% |
| the same engine, overnight | 3,788 | 0.36 | 81.6% | 18.4% | 98.7% |
| the same engine, five hours | 14,754 | 0.82 | 90.4% | 9.6% | 96.4% |
| with the round-bound changes, seven minutes | 1,068 | 2.4 | 93.8% | 6.2% | 87.7% |
| the engine without them again, an hour and a half | 5,657 | 1.2 | 92.7% | 7.3% | 94.6% |

The median height took 34 to 51 ms in every stretch. What differed is how often a
height stalled and for how long (without the round-bound changes 2.9% of all heights go
through five rounds or more, with them 0.2%), and that alone moved the pace from 0.4 to
2.4 blocks a second with the machines and the chain unchanged.
`TestCadenceReport` (`consensus/bft/cadence_test.go`) runs two real engines on a rig
that reproduces it: with the same height of about 35 ms and the stalls of a running pair,
the engine without the round-bound changes commits 0.4 to 0.9 blocks a second and the
one with them 1.6 to 3.4.

Why heights stall: the two validators leave a height in different rounds. After a
validator commits a block itself it starts the next height in round 1; after the node
takes a block from its peer it starts in round 0. A validator sends nothing in a round in
which it neither proposes nor has received a proposal, so validators a round apart hear
nothing from each other until a round comes whose proposer is the one that is ahead;
the one behind then moves up to it at once (the round-bound changes added that; before
them it got there only when its own round timer happened to bring it there). In the
rig every stalled height follows a height one of the validators took through the sync
path, and none stalls otherwise (0 of 2,158 heights in six runs); with the round-bound
changes about half of the heights that follow one stall, without them all do.

## The minimum block interval

`[consensus] MinBlockInterval` (`bft.WithMinBlockInterval`) is the least time a
validator lets pass between seeing a block commit and starting the round of the next
height: before it proposes, votes or starts that round's timers.

* The default is one second. `0s` turns the wait off and the blocks then come as fast as
  the validators can make them; a peer that is not a configured persistent peer is
  disconnected when the messages of that many blocks pass the p2p rate limit (32 a
  second with a burst of 200 by default).
* It is local to the validator. It is not a rule a block is checked against, it puts
  nothing in a message and changes nothing a validator signs or commits. Validators
  with different values, or one with none, take part in the same rounds, so a change
  needs no coordination beyond restarting the validators.
* It is at most half the commit timeout (2 s at the default 4 s). A longer value is
  refused by `consensusd` at start-up and lowered, with a warning, by the engine in
  the other binaries: a peer's round has to still be open when a proposal delayed by
  the interval reaches it.
* It waits only for what is left of the interval since the last commit this validator
  saw. It does not delay a round that follows a failed one, nor the first round of a
  validator that has just started, and a validator that has just taken the last blocks
  from its peer waits at most the interval after the last of them.
* The pace with it is at most one block per interval. The stalls are not removed by it
  (see above), so the pace over minutes is a little below that. On the rig, with the
  stalls of a running pair:

| interval | blocks a second | heights that stalled |
| --- | --- | --- |
| none, without the round-bound changes | 0.4 to 0.9 | 7 to 14% |
| none, with the round-bound changes | 1.6 to 3.4 | 3.5 to 7% |
| 500 ms | 1.0 to 1.2 | 4.5 to 7% |
| 750 ms | 0.84 to 1.0 | 3 to 6% |
| 1 s (default) | 0.69 to 0.84 | 2 to 5% |
| 1.25 s | 0.57 to 0.74 | 1 to 5% |
| 2 s | 0.42 to 0.44 | 4 to 5.5% |

(Two settings of the rig's hiccups, two or three runs of 400 heights for each figure. The
rig reproduces how many heights stall, not the pace to the last digit: for the engine
without the round-bound changes it gives 0.4 to 0.9 where the running pair kept 0.8.
Pace is about `1 / (interval + 0.04 s + the cost of the stalls per height)`, and the
stalls cost about 0.3 s a height with the round-bound changes.)

To slow the chain to the 2.5 s a block that the reward schedule was written for, set
`CommitTimeout` to at least 5 s and `MinBlockInterval` to 2.5 s.

## What counts blocks

Every quantity below is counted in blocks, so it moves with the pace. Rules counted in
time (staking APR and index, unbonding, governance voting periods, loyalty epochs, quota
epochs, sponsorship days, subscription and fixed-term billing days, heartbeat spacing,
swap and escrow expiry) read the block timestamp and do not.

| quantity | where | at 1 block/s | at 0.8 | at 2.5 |
| --- | --- | --- | --- | --- |
| validator epoch (100 blocks): rewards settled, validators re-selected, loyalty smoothing stepped | `core/epochs.go` `ProcessBlockLifecycle`, `finalizeEpoch`; `core/epoch/config.go` `DefaultConfig` | 100 s | 125 s | 40 s |
| emission per epoch (200 ZNHB to validators, stakers and engagement); the halving era is 500,000 epochs, written for 2.5 s a block (4 years) | `core/rewards/halving.go` `HalvingEmissionForEpoch`; `core/rewards_logic.go` `accrueEpochRewards`, `settleEpochRewards` | 7,200 ZNHB an hour, era 1.6 years | 5,760, 2.0 years | 18,000, 0.6 years |
| POTSO reward epoch (120 blocks, 50 ZNHB budget) | `core/state_transition.go` `maybeProcessPotsoRewards`; `native/potso/rewards.go` `RewardConfig.EpochLengthBlocks` | 120 s | 150 s | 48 s |
| buyback settlement, and the signed reference price it needs, per validator epoch | `core/buyback_settlement.go` `currentBuybackEpoch`, `settleBuybackEpoch` | 100 s | 125 s | 40 s |
| lending interest: `blocksPerYear` (31,536,000) counts a block as a second | `native/lending/engine.go` `accrueInterest`; `native/lending/math.go` `rateFactor`, `computeInterest` | as written | 20% under | 2.5 times over |
| lending oracle staleness bound (1,000 blocks); borrow cap per block | `native/lending/engine.go` (`MaxAgeBlocks`); `native/lending/params.go` `BorrowCaps.PerBlock` | 17 min | 21 min | 6.7 min |
| equivocation evidence window (8,640 blocks, a day at 10 s a block) | `consensus/potso/evidence/types.go` `DefaultMaxAgeBlocks`; `core/potso_evidence_tx.go` | 2.4 h | 3.0 h | 58 min |
| epoch snapshots kept (64 epochs) | `core/epochs.go` `pruneEpochHistory` | 1.8 h | 2.2 h | 43 min |
| transaction expiry: a `MaxBlockHeight` 100 blocks ahead, as the sender chose it | `core/node.go` `addTransaction`; `core/state_transition.go` `ApplyTransaction` | 100 s | 125 s | 40 s |
| storage (about 1.5 KB an empty block) and the work every block does (supply invariant, lifecycle, signatures) | `core/epochs.go` `ProcessBlockLifecycle` | 130 MB a day | 104 MB | 324 MB |

Block timestamps are whole seconds and never earlier than the block before
(`core/node.go` `validateBlockTimestamp`): above one block a second several blocks share
a second (up to twenty were seen in a burst). With an interval of one second or more the
timestamps of consecutive blocks increase on a healthy chain, since a block is built at
least the interval after the last one.
