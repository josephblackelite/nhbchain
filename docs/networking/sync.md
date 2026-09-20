# Fast sync workflow

## Status: not wired into the node

The `core/sync` package implements the pieces described below (manifest verification, resumable chunk download, an atomic
database swap that keeps a `.bak` copy, block-proof range sync), but the node does not use them to start or to recover. Nothing
in the node signs a snapshot manifest, so there is nothing for the quorum check to verify; nothing calls the install helpers;
and the governance anchor is never configured.

The two RPC methods that reached into the package are retired: `sync_snapshot_import` and `sync_snapshot_export` answer HTTP
410 with JSON-RPC error `-32060` and do nothing. The import replaced the running node's state in place with no backup and no
update of the chain head, and the node put the state back at its committed head at the next block or restart; the export wrote
unsigned files to a directory the caller named. `sync_status` is unchanged: it reports the chain height and the sync manager's
snapshot height, and `managerReady` says whether the manager exists.

A new validator is brought up from a verified snapshot made and checked outside the node, not through these methods. The
sections below describe what the package does when its pieces are driven by such a tool.

Fast sync brings a node from genesis to the current tip in two phases: snapshot import and range synchronization.

## Phase 1 – snapshot import

1. Fetch the manifest over HTTPS with TLS fingerprint pinning (the node checks the provided SHA-256 fingerprint).
2. Verify manifest signatures – at least 2/3 of the validator voting power must sign the digest. A governance anchor may be
   provided as a fallback trust root.
3. Download chunk files (resumable). Each file is hashed after download; corrupt files are retried.
4. Apply the snapshot in a temporary database and atomically swap it in for the live DB. A `.bak` copy of the original database
   is retained until the node finalizes the new state.
5. Reset the state processor to the imported root and persist the snapshot height as the fast-sync checkpoint.

## Phase 2 – range sync

Starting from the snapshot checkpoint the node requests block proofs from peers and verifies them before applying headers to the
local chain. A simplified flow:

```
checkpoint -> request proof (height+1) -> verify header linkage -> verify validator quorum -> apply header -> repeat
```

The range syncer stops once the fetcher signals `EOF`. Verified headers are optionally persisted by the caller to close the gap
between the snapshot height and the current tip.

## Operator checklist

* Validate the manifest digest and signature quorum before importing.
* Confirm the `chainId`, `height`, and `stateRoot` match the intended network.
* Ensure the snapshot directory has sufficient disk space (chunk size defaults to 16 MiB).
* After a snapshot start, monitor `sync_status` (or the block height) until the node catches up to the network tip.
* Retain the `.bak` database until the range sync has finalized and the node survives a restart.
