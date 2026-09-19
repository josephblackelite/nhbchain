# Snapshot audit checklist

This guide covers the checks an auditor can run on a snapshot before attesting to it. It follows the format written by `core/sync/snapshot_writer.go` and the verification in `core/sync/manifest_verify.go` and `core/sync/snapshot_loader.go`. The user-facing description of the format is in [../networking/snapshots.md](../networking/snapshots.md).

## Inputs

* The snapshot manifest, the JSON object returned by the `sync_snapshot_export` RPC (`core/sync/manifest.go`). Byte fields (`stateRoot`, `checkpoint`, chunk `hash`, signature `address` and `signature`) are base64 strings, as Go's JSON encoder writes `[]byte`.
* The chunk files, named `chunk-0000.bin`, `chunk-0001.bin`, and so on (`chunk-%04d.bin`).
* The validator set (addresses and voting power) at the advertised height.

`sync_snapshot_export` returns the manifest without signatures. Nothing in this repository signs a manifest, so signatures come from whoever publishes the snapshot.

## Manifest fields

`version` (currently `1`), `chainId`, `height`, `stateRoot`, `checkpoint`, `chunkSize`, `totalEntries`, `totalBytes`, `chunks[]` (`index`, `path`, `entries`, `bytes`, `hash`), `signatures[]` (`address`, `signature`, `weight`), optional `governance` (`payload`, `signature`) and `metadata`. On export, `checkpoint` is the hash of the chain's current block header and `metadata` holds `createdAt`, `stateRootHex`, `checkpointHeight` and `checkpointHash`.

## Procedure

1. **Chain context.** Confirm `chainId`, `height` and `stateRoot` match the expected network and checkpoint. Import rejects a non-zero `chainId` that differs from the node's.
2. **Digest.** The digest is the SHA-256 of Go's `json.Marshal` of the manifest struct with `signatures` set to nil, that is, serialised as `"signatures":null` rather than removed, in struct field order (`SnapshotManifest.Digest`). Recompute it with that method instead of editing the JSON by hand.
3. **Signature quorum.** Each entry in `signatures` must carry a 65-byte recoverable secp256k1 signature over the digest whose recovered address equals `address` and belongs to the validator set (an unknown validator or wrong length fails the whole check). Duplicate addresses count once. The signed voting power, taken from the validator set and not from the `weight` field, must satisfy `signed * 3 >= total * 2` (`ValidatorSet.VerifyQuorum` in `core/sync/blockproof.go`). A manifest with no signatures is accepted only if a governance verifier is configured; the node never configures one (`SetGovernanceVerifier` has no callers outside `core/sync`), so an unsigned manifest is rejected with `snapshot manifest missing validator signatures`.
4. **Chunk integrity.** For every entry in `chunks`, ordered by `index`, compute the SHA-256 of the file and compare it with `hash`. Records are `uint32 keyLen | key | uint32 valueLen | value` with big-endian lengths; the number of records must equal `entries` when that value is non-zero.
5. **State reconstruction.** Replay the records into an empty trie in an isolated database and compare the resulting root with `stateRoot` (`SnapshotLoader.Apply` fails with `state root mismatch` otherwise). Do not run this against a live node's database.
6. **Checkpoint header.** `checkpoint` is a block-header hash. Import does not check it beyond including it in the signed digest, so compare it with the canonical header hash at `height` (`metadata.checkpointHeight`) from chain data.
7. **Report.** Record the digest, the quorum summary and any anomalies. Publish the snapshot only when every check passes.

Keep the verification logs and the reconstructed state root so others can reproduce the result.
