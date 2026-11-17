# Fast sync components

`core/sync/` contains the pieces of a snapshot-plus-range fast sync. Which parts a
running node actually uses is stated per component below; the snapshot format is
in [snapshots.md](snapshots.md).

## Status: not used to start or recover a node

The `core/sync` package implements the pieces described here (manifest
verification, resumable chunk download, an atomic database swap that keeps a
`.bak` copy, block-proof range sync), but the node does not use them to start or to
recover. Nothing in the node signs a snapshot manifest, so there is nothing for the
quorum check to verify; nothing calls the install helpers; and the governance
verifier (`SetGovernanceVerifier`) is never set outside tests.

A new validator is brought up from a verified snapshot made and checked outside
the node, with `scripts/make-snapshot.sh`, `cmd/nhb-snapshot` and
`scripts/deployvalidator.sh`; see
[Onboarding a validator from a snapshot](../validators/snapshot-onboarding.md).
That tool has its own manifest and archive format, not the one in
[snapshots.md](snapshots.md).

## What is wired to the node

- `Node` creates a `sync.Manager` at start (`core/node.go`) with the chain ID, the
  current height and the trie database, and a validator set built from the active
  validators' voting power. It updates the manager's height as blocks commit.
- `sync_status` (`rpc/sync_handlers.go`) returns
  `{"chainHeight": n, "snapshotHeight": n, "managerReady": bool}`;
  `managerReady` says whether the manager exists.
- The two RPC methods that reached into the package, `sync_snapshot_import` and
  `sync_snapshot_export`, are retired: they answer HTTP 410 with JSON-RPC error
  `-32060` (`codeMethodDisabled`) and do nothing. The import used to replace the
  running node's state in place with no backup and no update of the chain head
  (the node put the state back at its committed head at the next block or restart),
  and the export wrote unsigned files to a directory the caller named.

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
- Quorum: the signatures' voting power must be a quorum of the manager's validator
  set, strictly more than two thirds (`types.HasQuorum`, `floor(2 * total / 3) + 1`,
  summed as big integers); unknown signers, wrong-length signatures and signatures
  that do not recover to the signer's address fail the proof.
- The syncer stops when the proof fetcher returns `io.EOF` and returns the last
  verified header. Headers are handed to a `HeaderApplier` as they verify.

Nothing in the node, the CLI or the RPC layer calls `RangeSync`, `EnsureChunks`,
`HTTPFetcher` or `InstallSnapshot`; only tests do. A running node catches up by
ordinary block sync over P2P (the `GetStatus`/`GetBlocks` messages in
[overview.md](overview.md#wire-format)), where blocks above
`QuorumCertActivationHeight` must carry a verified quorum certificate. The node
does not sync this chain from genesis; see
[Onboarding a validator from a snapshot](../validators/snapshot-onboarding.md#why-block-sync-from-genesis-is-not-supported).

## If you drive the package from a tool

- Check the manifest's `chainId`, `height` and `stateRoot` against the network you
  intend to join before importing.
- Confirm the manifest carries validator signatures; an unsigned manifest is
  rejected unless a governance verifier is set.
- After a snapshot start, watch `sync_status` (or the block height) until the node
  reaches the network tip.
