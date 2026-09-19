# Snapshot format

A node can export its state trie to a directory of chunk files plus a manifest,
and can import such a set (`core/sync/`). This page describes the format and the
two JSON-RPC methods that use it. The related range-sync code is described in
[sync.md](sync.md).

## Manifest

`SnapshotManifest` (`core/sync/manifest.go`), JSON:

| Field | Meaning |
| --- | --- |
| `version` | `1` (`ManifestVersion`). |
| `chainId` | Chain ID (first 8 bytes of the genesis hash). Import fails if it is non-zero and differs from the node's. |
| `height` | Block height of the exported state. |
| `stateRoot` | State root the chunks must rebuild. |
| `checkpoint` | Header hash of the block at that height (set by `Node.SnapshotExport`). |
| `chunkSize` | Target chunk size in bytes (16 MiB by default). |
| `totalEntries`, `totalBytes` | Sums over all chunks. |
| `chunks` | Ordered list of `{index, path, entries, bytes, hash}`; `hash` is the SHA-256 of the chunk file. |
| `signatures` | List of `{address, signature, weight}`: validator signatures over the manifest digest. |
| `governance` | Optional `{payload, signature}` anchor used instead of validator signatures. |
| `metadata` | Map: `createdAt`, `stateRootHex`, and, from `Node.SnapshotExport`, `checkpointHeight` and `checkpointHash`. |

The manifest digest is the SHA-256 of the manifest's JSON encoding with
`signatures` set to null (`Manifest.Digest`). Validators sign that digest with
their consensus secp256k1 key; signatures are 65 bytes and are verified by
public-key recovery.

## Chunk files

Chunks are named `chunk-0000.bin`, `chunk-0001.bin`, ... and contain records back
to back (`snapshot_writer.go`, `writeRecord`):

```
uint32 keyLen (big-endian) | key bytes | uint32 valueLen (big-endian) | value bytes
```

The writer walks the state trie with a trie iterator and stores each leaf's key
and value as the iterator returns them. It starts a new chunk once the written
size reaches `chunkSize`. It hashes each file with SHA-256 for the manifest.

## Verification and import

`Manager.ImportSnapshot` (`core/sync/manager.go`):

1. If the manifest's `chainId` is non-zero it must equal the node's chain ID.
2. `VerifyManifest`: if the manifest has signatures, the combined voting power of
   the signers, taken from the node's current validator set (the `weight` field
   in the manifest is not used), must be at least two thirds of the total
   (`signed * 3 >= total * 2`), every signer must be in that set, and every
   signature must recover to the signer's address. If it has no signatures, a
   governance verifier is required and must accept `governance`; the node does not
   install a governance verifier (`SetGovernanceVerifier` has no caller outside
   tests), so an unsigned manifest is rejected with
   `snapshot manifest missing validator signatures`.
3. `SnapshotLoader.Apply`: for each chunk in index order, check the SHA-256 of
   the file against the manifest, replay its records into an empty trie, and
   check the entry count when the manifest gives one. After all chunks it commits
   the trie and compares the root to `stateRoot` (`state root mismatch` on
   failure).

## JSON-RPC

Both methods require a bearer JWT (`rpc/sync_handlers.go`).

- `sync_snapshot_export` with one parameter object `{"outDir": "<dir>"}` writes
  the chunks under `outDir` and returns the manifest, with `checkpoint` and
  `metadata` filled in and **no signatures**. Nothing in this repository signs the
  manifest, so signatures must be added by other tooling before it can pass
  verification on import.
- `sync_snapshot_import` with `{"chunkDir": "<dir>", "manifest": {...}}` runs the
  import above against the node's live trie database, then resets the node's
  state processor to the imported root, reloads module pauses, records the
  manifest height and refreshes the sync validator set. It returns
  `{"stateRoot": "0x..."}`. Errors are HTTP 500, code `-32061`, message
  `snapshot_error`; missing or malformed parameters are HTTP 400, code `-32060`.
- `sync_status` (no auth, no parameters) returns `chainHeight`, `snapshotHeight`
  and `managerReady`.

## Library helpers that are not wired to a node command

`core/sync/snapshot_loader.go` also contains `HTTPFetcher` (downloads with an
optional SHA-256 TLS-certificate pin), `EnsureChunks` (re-downloads only missing
or hash-mismatched chunks) and `InstallSnapshot` (imports into a fresh database
directory `<target>.tmp`, then renames the existing directory to `<target>.bak`
and the new one into place). No node, CLI or RPC code calls them; they are
exercised by tests only.

## Failure modes

- Chain ID mismatch.
- No signatures and no governance verifier, or signed power below two thirds, or a
  signer outside the validator set, or a signature that does not recover to its
  address.
- A chunk file whose hash does not match, or a wrong entry count.
- State root mismatch after replay.
