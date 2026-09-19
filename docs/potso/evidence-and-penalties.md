# POTSO Evidence and Penalties

Misbehaviour reports ("evidence") are recorded on-chain by a dedicated transaction type, and a penalty engine runs during block processing. Code: `consensus/potso/evidence`, `consensus/potso/penalty`, `core/potso_evidence_tx.go`, `core/node.go`, `rpc/modules/potso_evidence.go`.

## Submission path

`potso_submitEvidence` builds a `TxTypeSubmitEvidence` (`0x4C`) transaction whose `Data` is the RLP-encoded evidence, with no envelope signature, `GasLimit` 0 and `GasPrice` 0 (`Node.PotsoSubmitEvidence`, `RequiresSignature` in `core/types/transaction.go`), and passes it to `AddTransaction`. If the record already exists, `PotsoSubmitEvidence` returns the `idempotent` receipt before building a transaction. Otherwise `AddTransaction` runs `validateTransaction`, which simulates the transaction against a copy of state while `Node.txSimulationEnabled` is true (`core/node.go`). It defaults to true, and `SetTransactionSimulationEnabled` is called only from tests (no other caller in the repository), so on a running node an invalid report is rejected synchronously. If simulation were switched off, `validateTransaction` would return before executing the transaction and validation would happen only when a block containing it is applied. A valid one is queued in the mempool and gossiped. It is recorded in the state trie only when a block containing it is applied (`applySubmitEvidenceTransaction`). The RPC result therefore means "admitted", not "recorded"; `potso_getEvidence` returns "evidence not found" until the block is applied.

The POTSO module pause (`system/pauses`, module `potso`) blocks both the RPC and the transaction.

## Evidence payload

| Field | Type | Notes |
| --- | --- | --- |
| `type` | string | Case-insensitive on input, stored upper-case. One of `DOWNTIME`, `EQUIVOCATION`, `INVALID_BLOCK_PROPOSAL`. |
| `offender` | string | Bech32 address of the accused. Must not be the zero address. |
| `heights` | array of uint64 | At least one. Ascending order (equal neighbours are allowed). |
| `details` | JSON | Raw bytes, hashed. For `EQUIVOCATION` it must be an equivocation proof (below). |
| `reporter` | string | Bech32 address of the reporter. Must not be the zero address. |
| `reporterSig` | hex | 65-byte secp256k1 signature. |
| `timestamp` | int64 | Included in the signing digest. Must be non-negative. |

There is no reporter allow-list: any address whose key signs the payload can report.

### Canonical hash and signature

```
hash   = BLAKE3-256( uint32be(len(TYPE)) || TYPE || offender(20 bytes)
                     || uint32be(len(heights)) || heights (each uint64 big-endian, sorted ascending)
                     || uint32be(len(details)) || details )
digest = SHA-256( "potso_evidence|" || hex(hash) || "|" || decimal(timestamp) )
```

`hex(hash)` has no `0x` prefix. The reporter signs `digest`; the recovered address must equal `reporter` (`CanonicalHash`, `SigningDigest`, `ValidateEvidence`).

### Equivocation proof

For `EQUIVOCATION`, `details` must be JSON `{"height": H, "round": R, "voteType": 1|2, "voteA": {"blockHash": "hex", "signature": "hex"}, "voteB": {...}}` (`1` prevote, `2` precommit). Each vote's signature must be a 65-byte signature over `SHA-256(JSON({"blockHash", "round", "type", "height"}))` (the BFT vote payload) that recovers to the offender's address, and the two block hashes must differ (`VerifyEquivocationProof`). `DOWNTIME` and `INVALID_BLOCK_PROPOSAL` have no proof requirement beyond the reporter signature.

## Validation

`ValidateEvidence` runs in the transaction path with the height of the block being applied and `DefaultMaxAgeBlocks = 8640`. It rejects, with these reasons: `invalid_type`, `invalid_offender`, `invalid_reporter`, `empty_heights`, `unsorted_heights`, `future_height` (a height greater than the current block height), `expired` (a height more than 8640 blocks old), `invalid_signature` (length, recovery or reporter mismatch) and `invalid_equivocation_proof`. `unknown_height` is defined but is not checked in this path, because no height lookup is supplied.

Only an `*evidence.ValidationError` from `AddTransaction` becomes a rejected receipt, and only that case returns HTTP 400, code `-32602`, message = the validation message, and `data = {"hash": "0x..."}` (`rpc/modules/potso_evidence.go`, `Submit`). Every other error from `Node.PotsoSubmitEvidence` (module paused, nonce or mempool errors, a negative `timestamp`, which `Evidence.EncodeRLP` refuses, and so on) is returned as HTTP 500, code `-32000`, with the error text as the message. Malformed request fields (missing or unknown `type`, bad addresses, bad `reporterSig` hex) return HTTP 400, code `-32602`, before the node is called. No `potso.evidence.rejected` event is emitted by any code path.

## Persistence and queries

Accepted records are stored at `potso/evidence/record/<hash>` (`potsoEvidenceRecordKey`, written with `KVPut` on the plain key, so under a single `Keccak256(key)`) and the hash is appended to `potso/evidence/pending` (`KVAppend`, also a single `Keccak256`). Resubmitting an already-recorded hash is a no-op; the RPC reports `status: "idempotent"` if the record already exists when it is called.

RPC (no authentication):

- `potso_submitEvidence(EvidencePayload) -> { hash, status }`, where `status` is `accepted` or `idempotent`.
- `potso_getEvidence({ hash }) -> record`, where the record has `hash`, `type`, `offender`, `heights`, `details`, `reporter`, `reporterSig`, `timestamp`, `receivedAt` (block timestamp). Unknown hash: HTTP 400, "evidence not found".
- `potso_listEvidence({ offender?, type?, fromHeight?, toHeight?, page: { offset?, limit? } }) -> { records, nextOffset? }`. Newest first; `limit` defaults to 50. `fromHeight` / `toHeight` compare against the record's smallest height.

Event on acceptance (`core/events/potso_evidence.go`): `potso.evidence.accepted` with `hash`, `type`, `offender`, `height` (smallest height), `reporter`.

## Penalty processing

`Node.processPendingEvidenceForState` runs after block lifecycle processing in `CreateBlock`, `ValidateBlock` and `CommitBlock`. For each recorded report whose smallest height is not older than `currentHeight - 8640`, it refreshes the offender's base weight from the account's current `Stake` (`Ledger.EnsureBaseline`) and calls `penalty.Engine.Apply`. The node builds the engine from `penalty.DefaultConfig()` with `SlashEnabled = true` and `EquivocationSlashBps = 10000`, and always passes `MissedEpochs = 0`.

Rules (`consensus/potso/penalty/rules.go`, amounts in wei):

| Type | Severity | Decay applied to the weight ledger | Slash |
| --- | --- | --- | --- |
| `EQUIVOCATION` | CRITICAL | `min(current, max(base * 5000 / 10000, 100))` | `base * 10000 / 10000` (all of the base weight) with the node's settings |
| `DOWNTIME` | MEDIUM | Ladder by missed epochs: 1 -> 200 bps, 2 -> 500 bps, 3+ -> 1000 bps of the current weight. With `MissedEpochs = 0` the decay is 0. | none |
| `INVALID_BLOCK_PROPOSAL` | HIGH | 300 bps of the current weight | none |

`penalty.Config` also has cooldown fields (7, 1 and 1 epochs); nothing in `Apply` reads them.

The slash (`state/bank.ValidatorSlasher.Slash`) reduces the offender account's `LockedZNHB` by up to the slash amount (capped at the locked balance), reduces `Stake` by the same amount, and credits the amount to the escrow fee treasury account. The `slashAmt` value in the event is the computed amount, not the capped amount.

Idempotency is per `(evidence hash, offender)`. The record is marked in `state/potso.Ledger`, which is held in memory by the node.

### Event

`potso.penalty.applied`: `hash`, `type`, `offender`, `decayPct` (basis points rendered as a percentage with two decimals), `slashAmt`, `newWeight`, `block`, `idempotent`. The node appends this event only for non-idempotent applications. `nhb_getSlashingEvents` returns these events from the node's current event list.

## Not implemented

There is no appeals or dispute mechanism: no RPC method, transaction type, event or evidence type for it. There is no `potso.audit` log stream in the code.
