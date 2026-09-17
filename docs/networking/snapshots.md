# Snapshot distribution format

NHB nodes can now publish state snapshots that allow fresh nodes to fast-sync without replaying the full chain. A snapshot is
a directory containing binary chunk files and a JSON manifest.

## Manifest

The manifest is versioned (`version: 1`) and contains:

* `chainId`, `height`, and `stateRoot` for the captured state.
* `checkpoint` – the block header hash that should be used as the starting point for range sync.
* `chunks` – the ordered list of chunk files with their SHA-256 digests, sizes, and entry counts.
* `signatures` – validator signatures over the manifest digest. At least 2/3 of the voting power must sign the manifest for it
to be considered valid. A governance anchor can be supplied as a fallback if a super-majority is unavailable.
* Metadata bag for diagnostics (creation time, checkpoint hash/height, state root hex).

The manifest digest is the SHA-256 hash of the manifest JSON with the signatures removed. Validators sign this digest using the
same secp256k1 keys used for consensus.

## Chunk layout

Chunk files are simple concatenations of length-prefixed key/value pairs:

```
uint32 keyLen | key bytes | uint32 valueLen | value bytes
```

Keys are hashed trie keys (keccak pre-images) and values are raw RLP nodes from the state trie. The writer walks the canonical
trie and flushes a chunk once the target size (default 16 MiB) is reached. Each chunk is hashed with SHA-256 and the digest is
stored in the manifest.

## Verification & import

During import the loader verifies each chunk digest, rewinds the trie to an empty root, and replays the chunk records. The new
state root is committed to the local triedb and compared against the manifest root. Imports are atomic – data is written to a
fresh database path which atomically replaces the live database on success. Existing databases are preserved under a `.bak`
folder.

If a manifest fails signature validation or any chunk hash mismatches, the snapshot is rejected. Downloads are resumable: the
client verifies existing chunk files before fetching them and only downloads missing/invalid chunks.

## Failure modes

* Missing signatures or an undersigned manifest.
* Chain ID mismatch.
* Chunk hash mismatch or truncated files.
* State root mismatch after replay.
* Governance anchor signature mismatch.

Operators should verify the manifest and chunk hashes before import and retain the backup copy until the node has successfully
synced past the checkpoint.
