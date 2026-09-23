# Snapshot format

The `core/sync` package defines a state snapshot format that lets a fresh node
start without replaying the full chain. A snapshot is a directory containing
binary chunk files and a JSON manifest. The node itself does not publish or install
snapshots: nothing in it signs a manifest, and the `sync_snapshot_export` and
`sync_snapshot_import` RPC methods are retired (HTTP 410, error `-32060`; see
[Fast sync components](sync.md)). What follows describes the format and the checks
the package applies when a tool drives it. It is not the format of the snapshots a
new validator installs: those are made by `scripts/make-snapshot.sh` and checked by
`cmd/nhb-snapshot`, described in
[Onboarding a validator from a snapshot](../validators/snapshot-onboarding.md).

## Manifest

`SnapshotManifest` (`core/sync/manifest.go`), JSON:

| Field | Meaning |
| --- | --- |
| `version` | `1` (`ManifestVersion`). |
| `chainId` | Chain ID (first 8 bytes of the genesis hash). Import fails if it is non-zero and differs from the node's. |
| `height` | Block height of the exported state. |
| `stateRoot` | State root the chunks must rebuild. |
| `checkpoint` | Header hash of the block at that height. |
| `chunkSize` | Target chunk size in bytes (16 MiB by default). |
| `totalEntries`, `totalBytes` | Sums over all chunks. |
| `chunks` | Ordered list of `{index, path, entries, bytes, hash}`; `hash` is the SHA-256 of the chunk file. |
| `signatures` | List of `{address, signature, weight}`: validator signatures over the manifest digest. |
| `governance` | Optional `{payload, signature}` anchor used instead of validator signatures. |
| `metadata` | Map of free-form strings (the writer sets `createdAt` and `stateRootHex`). |

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
   in the manifest is not used), must be a quorum of the total, strictly more than
   two thirds (`types.HasQuorum`, `floor(2 * total / 3) + 1`), every signer must be
   in that set, and every signature must recover to the signer's address. If it has no signatures, a
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

- `sync_snapshot_export` and `sync_snapshot_import` are retired. Each answers every
  call, whatever the parameters, with HTTP 410, JSON-RPC code `-32060`
  (`codeMethodDisabled`) and a message saying the snapshot pipeline is incomplete
  (`rpc/sync_handlers.go`). The import overwrote the live state in place with no
  backup and no chain head update, and nothing in the node signs a manifest.
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
- No signatures and no governance verifier, or signed power that is not more than
  two thirds, or a signer outside the validator set, or a signature that does not recover to its
  address.
- A chunk file whose hash does not match, or a wrong entry count.
- State root mismatch after replay.
