# Fast sync workflow

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
* After import, monitor `sync_status` RPC until the node catches up to the network tip.
* Retain the `.bak` database until the range sync has finalized and the node survives a restart.
