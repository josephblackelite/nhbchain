# BFT Height Synchronisation

The BFT engine (`consensus/bft/bft.go`) aligns its own height with the node's
committed chain height when it is constructed, when it starts a new round, and
after it commits. A validator therefore always targets `chain height + 1`, even
when the chain advanced by some other path (for example a block adopted from a
peer during sync).

## Interface contract

The engine talks to the node through `bft.NodeInterface`
(`consensus/bft/interface.go`):

```go
GetMempool() []*types.Transaction
RequeueTransactions(txs []*types.Transaction)
CreateBlock(txs []*types.Transaction) (*types.Block, error)
ValidateBlock(block *types.Block) error
CommitBlock(block *types.Block) error
GetValidatorSet() map[string]*big.Int
GetAccount(addr []byte) (*types.Account, error)
GetLastCommitHash() []byte
GetHeight() uint64
```

`GetHeight()` returns the latest block height committed to the local chain. The
engine uses it in these places:

- **Construction (`NewEngine`).** `currentState.Height` is set to
  `node.GetHeight() + 1` with round 0. The first call of `startNewRound` (the
  top of every `runRound`) then moves the round on to 1 (see below).
- **Round start (`startNewRound`).** It calls `syncHeightWithNodeLocked`. If the
  engine's height is at or below the node height, it jumps to
  `node.GetHeight() + 1`, resets the round to 0 and drops buffered proposals and
  votes for heights at or below the node height, and then moves to round 1. If the
  engine did not need to jump: when the current height is already marked
  committed, it advances one height; otherwise it moves to the next round of the
  same height (`roundAfter`).
- **After a commit (`commit`).** Once `CommitBlock` succeeds the engine
  increments its height, resets the round to 0 and the lock state, refreshes the
  validator set and voting power from `node.GetValidatorSet()`, and calls
  `syncHeightWithNodeLocked` again.

`syncHeightWithNodeLocked` also deletes every `committedBlocks` entry whose
height is at or below the node height, so a stale entry cannot short-circuit a
later round.

`Start()` waits 30 seconds after it is called before it requests peer status and
runs its first round.

## The round a height starts in

A height starts in round 1 at every validator, whichever way the height before it
ended. After the engine's own commit, `commit` leaves round 0 and the next
`startNewRound` moves on one round from it (`roundAfter`), as it does for an
engine that has just been built; a height the node reaches because it took a
block from a peer (`syncHeightWithNodeLocked`, which sets round 0) is moved to
round 1 in the same way. Two validators that start a height in different rounds
hear nothing from each other until the round of the one behind times out.

`NotifyExternalCommit` records the commit for the minimum block interval and
signals the round loop without blocking (safe from any goroutine). The round loop
(`runRound`, and `waitMinBlockInterval`) ends the round in progress only when the
node's chain has reached the height of that round (`chainReached`). A peer's block
that reaches the node while the engine is committing the same one is answered
without an error and signalled all the same, and the engine has already counted it:
taken for news it would end the first round of the next height and start that
height one round ahead of the peer. See [Block cadence](block-cadence.md).

## Validators that are rounds apart after restarts

When every validator that runs is needed for a quorum -- three of three, three of four with
one down, five of seven with two down -- a block commits only in a round that all of them are
in. A restarted validator starts again in the first rounds of the height (or after the last
round it signed in) while the others go on, a round timeout at a time, so after restarts that
overlap one validator can be rounds ahead of the others, which are level with each other. A
round lasts as long at every validator, so it stays ahead, and a validator sends nothing in a
round in which it has no proposal to vote on: the height stopped and the round numbers went on
up. A pair of validators never showed it, nor did a set with a quorum to spare (two
validators, or four with none down).

The cause was the rule that moves a validator to a later round (`supportedRoundLocked`): only
when the validators seen there hold a third of the power (before the rule above: more than a
third and, with its own, a quorum). One validator alone ahead holds less than a third of a set
of four, so the others never followed it. A lock is
not the cause: the validators in a wedge often hold one, and it happens with none.

The rule now also follows the validators ahead when this validator and the ones it has heard
from in its own round, or in the `maxFutureRounds` before it, are not a quorum and they and the
validators ahead are: no round can decide without a validator that is ahead, and it does not
come back to a round it has left. A validator that has a quorum in its own round does not move,
so one validator, or a group below a third, cannot take the round from a quorum in step. To
know who is there the engine records the messages for its own round or an earlier one
(`noteHeard`), as it records those for a later one. Nothing that is sent, signed, locked or
committed changes: a move of rounds is not a vote, the vote record still puts a validator after
the last round it signed in, and the lock is left as it is.

This last rule holds no floor on how little power the validators ahead need to hold -- down to
one holding far less than a third, alone -- because at the boundary it is built for, a validator
under a third looks exactly like the one real validator a restarted set is legitimately missing.
What bounds it instead is how many distinct rounds it will move a validator through at one
height: at most `maxFutureRounds` of them (`admitWeakRoundJumpLocked`), however many times the
shortfall this rule acts on comes and goes while the height runs. A genuine restart needs the
rule only until this validator reaches the validators it was missing, after which a real quorum
no longer needs it; a false claim, repeated, does not stop there, and this keeps what it can cost
to no more rounds than a restarted validator is ever expected to need to rejoin. A round already
granted this way costs nothing more.

Two things about that bound do not follow from the paragraph above, and both were wrong in an
earlier version of this document: what it counts, and how long it lasts.

What it counts: only a round this validator actually moves to (`startNewRound`'s own call to
`supportedRoundLocked`) spends one of the `maxFutureRounds` slots. Deciding whether to wake the
round loop early when a future-round vote or proposal arrives (`admitFutureRoundLocked`'s
preview, `previewSupportedRoundLocked`) asks the identical question but spends nothing, however
many times it is asked and whatever it answers. The two used to be the same call, which meant a
burst of ordinary future-round messages -- costing their sender one message each, needing no
round of real time to pass and no restart at all -- could exhaust the whole height's bound before
the one real jump a quorum-completing validator needed ever asked for it, refusing that validator
for the rest of the height: the fix built to stop an unbounded drag could itself be turned into a
worse, message-cost-free version of the same stall. See
`round_sync_test.go`'s `TestABurstOfFutureRoundVotesDoesNotSpendTheWeakJumpBudget`.

How long it lasts: the bound is durable across a restart of this same validator, the same as the
lock and the sign state (`WithWeakJumpSnapshotPath`, wired in `cmd/consensusd/main.go` and
`cmd/nhb/main.go` next to their `WithLockSnapshotPath`/`WithSignStatePath`). Without that, a
restart of the validator the bound protects -- a crash, an upgrade, or an operator bouncing a
node that looks stuck at a wedged height, which is exactly what happens below -- reset the bound
to zero while the height itself had not changed, so the same lone claimant could win a fresh
`maxFutureRounds` on every restart of its victim: the identical unbounded drag this rule exists
to close, gated by restart count instead of by message count. See
`weak_jump_snapshot_test.go`'s `TestARestartOfTheDraggedValidatorDoesNotResetTheWeakJumpBudget`.

Neither gap shows up in the wedge-report numbers below: those restarts are paced by the round
timers of a real run, which only ever needs one grant per genuine catch-up (`supportedRoundLocked`
jumps straight to a peer's current highest claim), and 12 restarts of one run were not enough on
their own to walk the bound to its limit even without this fix. That is precisely why both gaps
needed their own tests instead of showing up here: this table measures whether restarts wedge a
set under realistic timing, not whether the bound they rely on holds under a burst of messages or
a long run of restarts, which is what the two tests above are for.

`TestWedgeReport` (`consensus/bft/restart_wedge_test.go`) counts the runs that wedge -- no block
for 80 rounds with every running validator running -- on real engines with 160 ms rounds, one,
two or all of the running validators restarted at random moments twelve times a run, the vote
record, the lock snapshot and the weak-jump budget all kept across each restart. Every run also
checks that no two chains hold different blocks and that no validator signs two conflicting votes.

Measured on this release, 12 runs of 12 restarts each (2026-11-10). "Before" is the same code with the
rule of the paragraph above switched off, so the rule of a third (see above) is in both columns:

| set | wedged before | wedged now |
| --- | --- | --- |
| three validators | 0 of 12 runs | 0 of 12 |
| four validators, one down | 10 of 12 runs | 0 of 12 |
| seven validators, two down | 12 of 12 runs | 0 of 12 |

No run forked and no validator signed two conflicting votes. The same test on the release before
the rule of a third measured the original change at: three validators 14 of 20 runs wedged before and
0 of 20 now, four validators with one down 51 of 60 and 0 of 60, seven validators with two down
60 of 60 and 0 of 60.

A set that runs the old engine at some of its validators is no worse than it was. It heals when
a validator that is behind runs the new one, which follows the validator that is ahead, and the
other validator that is behind then follows the two by the rule that was always there. With three
of four running, 51 of 60 runs wedged with none of them on the new engine, 9 of 30 with one and
none of 30 with two; three restarts that overlapped wedged 25 of 40 runs on the old engine and
none of 40 when each restarted validator took the new one.

## Rounds the engine will take a message for

Round numbers are bounded (`consensus/bft/round_sync.go`):

- The engine is never in a round above `maxRound` (`math.MaxInt32`), and what a
  message can move it to is capped at half of that (`maxJumpRound`).
- A proposal or vote for a future round is kept for replay only when it is within
  `maxFutureRounds` (8) of the round the engine is in, and the amount kept is
  bounded per validator and in total (2 proposals per validator per height, 8 per
  height in all; 16 votes per validator per height).
- In the round it is in, the engine validates a proposal once however many times
  it is queued, and at most `maxProposalsValidatedPerSigner` (4) different
  proposals of one signer. A further copy or proposal is not validated and is
  counted in the rate-limited `consensus_message_dropped` warning (reasons
  `proposal already validated in this round` and `too many different proposals
  from this signer in this round`). A re-proposal whose polka proof has more
  entries than there are validators is refused before any signature in it is
  checked, and the validator prevotes nil for it.
- What a signed message says about where its sender is is remembered as one round
  number per validator. The engine moves to a later round only when the
  validators seen in it hold at least a third of the voting power
  (`supportedRoundLocked`): a group of that size has an honest validator in it
  while less than a third of the power is faulty, and the validators outside it
  hold at most two thirds, which is not a quorum. A coalition below a third moves
  nobody. Round timers are the same for everyone, so validators split into groups a
  round or more apart (three equal validators with one ahead, five with two ahead)
  stay apart unless the group behind follows the group ahead. The engine moves to
  the highest round that is supported.
- On (re)start the engine skips every round it has already signed in
  (`signedRoundFloor`, from the vote record described below).

## Quorum rule the engine applies

`hasTwoThirdsPowerLocked` requires received power for a vote type to reach
`types.QuorumThreshold(totalVotingPower)` = `floor(2 * total / 3) + 1`
(`core/types/quorum.go`; strictly more than two thirds, so exactly two thirds is
not enough), where `totalVotingPower` is the sum of the validator set's weights.
`commit` runs only when that holds for precommits. An empty set or a non-positive
total never has a quorum. The committed block carries a `QuorumCert` built from
the precommit signatures (`buildQuorumCertLocked`), which peers that sync the
block later verify with the same threshold (see
[Consensus invariants](invariants.md)).

## Lock and vote persistence across restarts

When started through `cmd/nhb` or `cmd/consensusd`, the engine is created with
two files in the data directory (`bft.WithLockSnapshotPath`,
`bft.WithSignStatePath`):

- `<DataDir>/polc_lock.json`: the proof-of-lock state. At construction the engine
  restores it only if the snapshot's height equals the height the engine is about
  to contest; a snapshot for any other height is ignored. Because
  `currentState.Height` is derived from `GetHeight()`, a lock left over from an
  already-committed height is never reapplied.
- `<DataDir>/bft_sign_state.json`: the double-sign record, the last round this
  validator signed a vote in and the block hash it signed for each vote type
  (`consensus/bft/sign_state.go`). It is written before a vote is signed, and a
  vote whose record cannot be written is not signed. The engine refuses to sign a
  different value for a vote type in a round it has already voted in, and refuses
  to sign for an earlier round or height than the last vote signed. At
  construction the record is restored when its height is the one the engine is
  about to contest or a later one (a chain database that is behind the record
  keeps the validator silent until block sync reaches it); only a record for a
  height that has committed is dropped. A record that exists but cannot be read
  stops the engine signing any vote until the file is repaired or moved aside.

## Tests

`consensus/bft/bft_test.go` includes nodes whose `GetHeight()` returns a
non-zero height (for example `syncedHeightNode`) to check that the engine
resynchronises after the node height moves ahead.
