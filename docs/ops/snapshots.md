# Snapshot Operations Guide

State snapshots are produced and consumed through three JSON-RPC methods on the
node (`cmd/nhb`), implemented in `rpc/sync_handlers.go` and `core/sync/`. There is
no `snapshot` command in any CLI in this repository, and no state-sync flag on
`nhb` or `consensusd`.

## Methods

| Method | Auth | Params | Result |
| ------ | ---- | ------ | ------ |
| `sync_snapshot_export` | JWT (or client certificate) | `[{"outDir": "<path>"}]` | the snapshot manifest |
| `sync_snapshot_import` | JWT (or client certificate) | `[{"chunkDir": "<path>", "manifest": <manifest>}]` | `{"stateRoot": "0x..."}` |
| `sync_status` | none | none | `{"chainHeight", "snapshotHeight", "managerReady"}` |

Errors use the codes `-32060` (`invalid_params`) and `-32061` (`snapshot_error`).
`sync_status` reports `managerReady: false` when the fast-sync manager is not
initialised.

## Export

`sync_snapshot_export` walks the state trie at the current header's state root
and writes chunk files named `chunk-0000.bin`, `chunk-0001.bin`, ... into
`outDir`. The default chunk size is 16 MiB (`core/sync/snapshot_writer.go`).

The manifest (`core/sync/manifest.go`) is returned in the RPC response and is
not written to `outDir`; save it yourself. Its fields are `version` (1),
`chainId`, `height`, `stateRoot`, `checkpoint` (the header hash), `chunkSize`,
`totalEntries`, `totalBytes`, `chunks` (each with `index`, `path`, `entries`,
`bytes`, `hash`), `signatures`, an optional `governance` anchor, and `metadata`.
The exported manifest has an empty `signatures` list.

`metadata` is a string map. The writer sets `createdAt` (UTC, RFC 3339) and
`stateRootHex` (`0x` plus 64 hex digits; `core/sync/snapshot_writer.go`).
`Node.SnapshotExport` adds `checkpointHeight` (decimal) and `checkpointHash`
(hex without a `0x` prefix) only when the current header's hash could be
computed (`core/node.go`).

The manifest's byte fields are Go `[]byte` values, so they are encoded as
base64 in JSON, not hex: `stateRoot`, `checkpoint`, each chunk `hash`, and the
`address`, `signature`, `payload` fields inside `signatures` and `governance`
(`core/sync/manifest.go`). This differs from the `sync_snapshot_import` result,
which returns `stateRoot` as a `0x`-prefixed hex string (`rpc/sync_handlers.go`),
and from `metadata.stateRootHex`.

## Import

`sync_snapshot_import` (`Node.SnapshotImport`):

1. Rejects a manifest whose `chainId` is non-zero and differs from the node's.
2. Verifies the manifest with `VerifyManifest`: it must carry validator
   signatures that reach quorum against the node's validator set, or, when it has
   none, a governance anchor accepted by a configured governance verifier. A
   manifest without signatures and without a governance verifier is rejected with
   `snapshot manifest missing validator signatures`.
3. Checks each chunk's hash and entry count, rebuilds the trie, and requires the
   computed root to equal the manifest's `stateRoot` (`state root mismatch`
   otherwise).
4. Resets the node's state to that root, reloads the module pause flags and the
   validator set, and sets the fast-sync manager height to the manifest height.

The node does not register a governance verifier in `cmd/nhb` or `cmd/consensusd`
(`SetGovernanceVerifier` has no caller), so in practice a manifest must be signed
by a validator quorum before it can be imported. No code in this repository adds
signatures to an exported manifest.

## Operational notes

- Chunk files are plain files under the directory you choose. Distribute and
  verify them with your own tooling; the manifest's per-chunk `hash` and the
  loader's check are the only integrity checks in the code.
- `core/sync` also contains an HTTP chunk fetcher with TLS fingerprint pinning
  and an atomic database swap (`EnsureChunks`, `InstallSnapshot`), but no RPC or
  command in this repository calls them.
