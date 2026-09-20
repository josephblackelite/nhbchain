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
engine uses it in three places:

- **Construction (`NewEngine`, `bft.go` line 241).** `currentState.Height` is
  set to `node.GetHeight() + 1` with round 0.
- **Round start (`startNewRound`, line 1196).** It calls
  `syncHeightWithNodeLocked`. If the engine's height is at or below the node
  height, it jumps to `node.GetHeight() + 1`, resets the round to 0 and drops
  buffered proposals and votes for heights at or below the node height. If the
  engine did not need to jump: when the current height is already marked
  committed, it advances one height; otherwise it increments the round.
- **After a commit (`commit`, line 800).** Once `CommitBlock` succeeds the engine
  increments its height, resets the round and lock state, refreshes the
  validator set and voting power from `node.GetValidatorSet()`, and calls
  `syncHeightWithNodeLocked` again.

`syncHeightWithNodeLocked` (line 1267) also deletes every `committedBlocks`
entry whose height is at or below the node height, so a stale entry cannot
short-circuit a later round.

`NotifyExternalCommit` (line 917) lets the node tell the engine that a block was
committed by some path other than the engine's own round. It only signals
(non-blocking, safe from any goroutine); the engine re-reads the real height from
`GetHeight()` at its next round start.

## Quorum rule the engine applies

`hasTwoThirdsPowerLocked` (line 1182) requires received power for a vote type to
be at least `(2 * totalVotingPower + 2) / 3` (integer division), where
`totalVotingPower` is the sum of the validator set's weights. `commit` runs only
when that holds for precommits. The committed block carries a `QuorumCert` built
from the precommit signatures (`buildQuorumCertLocked`, line 853), which peers
that sync the block later verify (see
[Consensus invariants](invariants.md)).

## Lock persistence across restarts

When started through `cmd/nhb` or `cmd/consensusd`, the engine is created with a
lock snapshot path of `<DataDir>/polc_lock.json` (`bft.WithLockSnapshotPath`).
At construction it restores a persisted lock only if the snapshot's height equals
the height the engine is about to contest (`bft.go`, line 288 onward); a snapshot
for any other height is ignored. Because `currentState.Height` is derived from
`GetHeight()`, a lock left over from an already-committed height is never
reapplied.

## Tests

`consensus/bft/bft_test.go` includes nodes whose `GetHeight()` returns a
non-zero height (for example `syncedHeightNode`) to check that the engine
resynchronises after the node height moves ahead.
