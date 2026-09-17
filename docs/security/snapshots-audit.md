# Snapshot audit checklist

This guide covers the steps an auditor should follow before attesting to a published snapshot.

## Inputs

* Snapshot manifest (`manifest.json`).
* Chunk directory (`chunk-*.bin` files).
* Validator set public keys / voting power at the advertised height.
* Governance anchor key (if applicable).

## Procedure

1. **Chain context** – Confirm the manifest `chainId`, `height`, and `stateRoot` match the expected network and checkpoint.
2. **Digest verification** – Recompute the manifest digest (SHA-256 of the JSON with the `signatures` field removed).
3. **Signature quorum** – Verify each validator signature recovers the advertised address and tally the voting power. Ensure at
   least 2/3 of the total power signed the digest. If validator quorum is not met, verify the governance anchor signature and
   payload.
4. **Chunk integrity** – Hash every chunk file and compare against the manifest digest entries. Spot-check record decoding for
   malformed key/value pairs.
5. **State reconstruction** – Replay the snapshot into an isolated database and confirm the resulting state root matches the
   manifest `stateRoot`.
6. **Checkpoint header** – Hash the advertised checkpoint header (if supplied) and ensure it matches the manifest `checkpoint`
   field. Optionally verify the header against canonical chain data.
7. **Report** – Record the digest, quorum summary, and any anomalies. Only publish the snapshot once all checks succeed.

Auditors should retain their verification logs and the reconstructed state root so downstream operators can reproduce the audit
trail.
