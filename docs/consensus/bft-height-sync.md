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

## Rounds the engine will take a message for

Round numbers are bounded (`consensus/bft/round_sync.go`):

- The engine is never in a round above `maxRound` (`math.MaxInt32`), and what a
  message can move it to is capped at half of that (`maxJumpRound`).
- A proposal or vote for a future round is kept for replay only when it is within
  `maxFutureRounds` (8) of the round the engine is in, and the amount kept is
  bounded per validator and in total (2 proposals per validator per height, 8 per
  height in all; 16 votes per validator per height).
- What a signed message says about where its sender is is remembered as one round
  number per validator. The engine moves to a later round only when the
  validators seen in it hold strictly more than a third of the voting power and,
  together with this validator's own power, a quorum (`supportedRoundLocked`).
  It never moves on a single message, and it moves to the highest round that is
  supported.
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
  (`consensus/bft/sign_state.go`). It is written before a vote is signed. The
  engine refuses to sign a different value for a vote type in a round it has
  already voted in, and refuses to sign for an earlier round or height than the
  last vote signed.

## Tests

`consensus/bft/bft_test.go` includes nodes whose `GetHeight()` returns a
non-zero height (for example `syncedHeightNode`) to check that the engine
resynchronises after the node height moves ahead.
