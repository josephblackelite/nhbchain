# Fast sync components

`core/sync/` contains the pieces of a snapshot-plus-range fast sync. Which parts a
running node actually uses is stated per component below; the snapshot format is
in [snapshots.md](snapshots.md).

## What is wired to the node

- `Node` creates a `sync.Manager` at start (`core/node.go`, ~line 708) with the
  chain ID, the current height and the trie database, and a validator set built
  from the active validators' voting power. It updates the manager's height as
  blocks commit (~line 4138).
- JSON-RPC exposes `sync_snapshot_export`, `sync_snapshot_import` and
  `sync_status` (`rpc/sync_handlers.go`); see [snapshots.md](snapshots.md#json-rpc).
  `sync_status` returns
  `{"chainHeight": n, "snapshotHeight": n, "managerReady": bool}`.

## Range verification (library code)

`Manager.RangeSync` and `RangeSyncer` (`core/sync/blockproof.go`) verify a chain of
block proofs starting from a checkpoint header:

```
checkpoint -> fetch proof for height+1 -> check height and PrevHash linkage
           -> check validator quorum over the proof digest -> apply header -> repeat
```

Details:

- A proof is a header plus validator signatures (`BlockProof`).
- The signed digest is `sha256(chainID (8 bytes BE) || height (8 bytes BE) ||
  header hash)`.
- Quorum: the signatures' voting power must satisfy `signed * 3 >= total * 2`
  against the manager's validator set; unknown signers, wrong-length signatures
  and signatures that do not recover to the signer's address fail the proof.
- The syncer stops when the proof fetcher returns `io.EOF` and returns the last
  verified header. Headers are handed to a `HeaderApplier` as they verify.

Nothing in the node, the CLI or the RPC layer calls `RangeSync`, `EnsureChunks`,
`HTTPFetcher` or `InstallSnapshot`; only tests do. A running node catches up by
ordinary block sync over P2P (the `GetStatus`/`GetBlocks` messages in
[overview.md](overview.md#wire-format)), where blocks above
`QuorumCertActivationHeight` must carry a verified quorum certificate.

## Operator checklist for a snapshot import

- Check the manifest's `chainId`, `height` and `stateRoot` against the network you
  intend to join before importing.
- Confirm the manifest carries validator signatures; an unsigned manifest is
  rejected.
- After `sync_snapshot_import`, watch `sync_status` (`chainHeight`,
  `snapshotHeight`).
