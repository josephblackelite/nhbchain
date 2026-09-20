# POTSO Evidence Intake

Phase 3A introduces a canonical intake flow for POTSO misbehaviour evidence. This layer validates authenticity, enforces replay protection, and persists accepted records so that subsequent penalty logic can consume a deduplicated feed.

## Submitting evidence

Evidence is reported with an ordinary signed native transaction of type `TxTypeSubmitEvidence` (`0x4C`), sent through `nhb_sendTransaction` like every other signed transaction. There is no separate submit RPC method (`potso_submitEvidence` was removed): the reporter is always the transaction's recovered signer, and the transaction carries a nonce like any other.

`tx.Data` is the RLP encoding of the evidence value: the list `[type, offender, heights, details, reporter, reporterSig, timestamp]`, where `type` is the upper-case type name, `offender` and `reporter` are 20-byte addresses, `heights` is a list of unsigned integers, `details` and `reporterSig` are byte strings and `timestamp` is an unsigned integer. A Go client can use `rlp.EncodeToBytes(&evidence.Evidence{...})` from `nhbchain/consensus/potso/evidence`.

A submission is only accepted when:

* `reporter` in the payload is the account that signed the transaction.
* That account has at least the governed minimum validator stake bonded (its own locked ZNHB).
* The transaction nonce is the account's current nonce; it is consumed by an accepted report.
* The chain has room: at most 256 evidence records are held at once and one reporter may hold at most 4 live records.
* For `EQUIVOCATION`, the proof names a height that the report lists among its `heights` and that is no later than the block the report is applied in, and the same double-sign is not already recorded (see [Canonical hash & replay guard](#canonical-hash--replay-guard)).

A report that is refused for any of these reasons is an ordinary failed transaction and never affects the rest of the block.

## Evidence payload schema

Evidence submissions must include the following fields:

| Field | Type | Notes |
| --- | --- | --- |
| `type` | string | One of `DOWNTIME`, `EQUIVOCATION`, `INVALID_BLOCK_PROPOSAL`. |
| `offender` | string | NHB Bech32 address of the validator being accused. |
| `heights` | array<uint64> | Block heights relevant to the accusation (at most 64). Heights must be in ascending order. An `EQUIVOCATION` report must list the height its proof is about. |
| `details` | JSON | Free-form, reporter-controlled data (at most 2048 bytes). The raw bytes are part of the canonical hash. For `EQUIVOCATION` it is the proof. |
| `reporter` | string | NHB Bech32 address of the reporter. |
| `reporterSig` | hex | 65-byte secp256k1 signature authenticating the payload. |
| `timestamp` | int64 | Reporter clock in UNIX seconds; embedded into the signing digest. |

## Canonical hash & replay guard

Every payload is mapped to a canonical hash:

```
blake3(type || offender || len(heights) || heights || details)
```

`type` is upper-cased and ASCII encoded, addresses are raw 20-byte values, and heights are encoded as big-endian 64-bit integers prefixed by the list length. This hash is stable across reporters and serves two purposes:

* Replay protection - a submission whose hash is already recorded is refused and no new record is written.
* Query key – `potso_getEvidence` resolves records by canonical hash.

For `EQUIVOCATION` the hash alone is not enough to tell reports apart. It covers the heights the reporter lists and the exact bytes of the proof, so one double-sign written another way (the two votes swapped, a block hash in another case, the JSON laid out differently, a signature in its other valid encoding, another conflicting vote in place of one of the two, another height listed beside the proof's) has another hash, and would otherwise be a new first report of the same misdeed. A double-sign is therefore identified by its **offense**: the offender and the height, round and vote type the proof names. The votes a validator signs cover exactly those, so a proof for any other decision point cannot be produced without the offender's key. Each offense is recorded once: a report whose offense is already recorded is refused as already recorded, whatever its hash, and the penalty is applied and remembered per offense (see [Penalty math & idempotency](#penalty-math--idempotency)). Separate double-signs (another height, round or vote type) are separate offenses. A report of any other type is its own offense, identified by its canonical hash.

The signature domain uses the canonical hash and timestamp: reporters sign the SHA-256 digest of `"potso_evidence|<hash>|<timestamp>"`.

## Authenticity checks

The verifier enforces:

* Known evidence type.
* Non-zero offender and reporter addresses.
* Ascending `heights` list.
* Heights not in the future relative to the node's tip.
* Heights within the rolling window (`DefaultMaxAgeBlocks = 8640`).
* Heights that actually exist in the canonical chain.
* For `EQUIVOCATION`, that the height the proof names is one of the listed `heights` and is no later than the block the report is applied in. The window checks above look at the listed heights, which the reporter chooses, so this is what makes them apply to the double-sign itself: a proof for an older double-sign cannot be carried by a report that lists a recent height, and votes for a height the chain never reached are refused.
* At most 64 heights and 2048 bytes of `details`, and a non-negative `timestamp`.
* Valid 65-byte secp256k1 signature matching the reporter.
* For `EQUIVOCATION`, a proof that the offender's own key signed two conflicting votes.

A rejected submission fails as an ordinary transaction with a machine-readable reason such as `invalid_signature`, `expired` or `oversized`. A proof for a height the report does not list, or beyond the current height, is refused as `invalid_equivocation_proof`.

**Known limit.** The vote a validator signs carries no chain id (changing it would change consensus), so two votes signed with one validator key at the same height, round and vote type on two different chains prove a double-sign on either. A validator key must never be used on more than one chain. The evidence rules above bound the damage to one penalty per offense on this chain; they cannot tell the chains apart.

## Persistence & queries

Accepted submissions are stored in chain state with their canonical hash, full payload, and the block timestamp at which they were recorded. The set of live records is bounded: a record is removed once the height of its offense is older than `DefaultMaxAgeBlocks` (the same window that stops an old report being submitted), and no more than 256 records are held at any time. The height of the offense is the height the proof names for an `EQUIVOCATION` and the oldest height listed for any other report. A removed record cannot be submitted again, because the height of its offense is then expired and an `EQUIVOCATION` report has to list it; until then the offense stays recorded, so it cannot be reported afresh in between.

RPC surfaces two read-only endpoints under the POTSO namespace:

* `potso_getEvidence(hash) -> EvidenceRecord`
* `potso_listEvidence(filters?) -> { records, nextOffset? }`

Filters support `offender`, `type`, `fromHeight`, `toHeight`, and pagination via `page: { offset, limit }`; a page holds at most 200 records. Results expose raw `details` bytes exactly as submitted, the reporter signature, and the `receivedAt` block timestamp.

## Events

Two new topics are emitted:

* `potso.evidence.accepted { hash, type, offender, height, reporter }` for new records (the smallest referenced height is published).
* `potso.evidence.rejected { reason, reporter }` is defined but not currently emitted: a rejected submission fails as a transaction, and a failed transaction's events are discarded.

Downstream consumers can subscribe to these to trigger dashboards, alerting, or follow-on enforcement once penalty logic is wired up.

## Penalty math & idempotency

Phase 3B introduces a deterministic penalty engine that maps accepted evidence to participation weight decay and optional token slashing. The rules are table-driven per evidence type:

| Evidence type | Severity | Weight decay | Slash | Cooldown |
| --- | --- | --- | --- | --- |
| `EQUIVOCATION` | Critical | `max(θ_eq × baseWeight, minDecay)` | Optional `S_eq` basis points of base weight (feature-gated) | 7 epochs |
| `DOWNTIME` | Medium | Ladder: `θ_dt(N)` for `N` missed epochs (defaults: 2%, 5%, 10%) | None | 1 epoch |
| `INVALID_BLOCK_PROPOSAL` | High | Fixed percentage (default 3%) of current weight | None | 1 epoch |

Decay percentages are expressed in basis points and applied against the offender's participation weight. Results are clamped between configured floor and ceiling bounds to prevent negative or runaway values. When slashing is disabled, any computed slash amount is ignored but still surfaced to telemetry.

Every application is idempotent: the pair `{offense, offender}` is recorded in chain state together with the penalty it guards, so the record is versioned with the block that applied it. The offense is the identity described under [Canonical hash & replay guard](#canonical-hash--replay-guard), not the hash of whichever report brought it to light, so no other way of writing the same double-sign is penalised again. Every node computes the same result whether it is building, validating, committing or replaying a block, and a trial build that is discarded leaves nothing behind. The record is removed together with the evidence record when that ages out of the window.

A slash moves the offender's locked ZNHB to the treasury (the chain's admin wallet). Because that wallet's balance is covered by the sale and reward pools, the forfeited amount is added to the reward pool in the same step, so the ZNHB supply invariant holds across a slash.

### Penalty events

Successful executions emit `potso.penalty.applied { hash, type, offender, decayPct, slashAmt, newWeight, block, idempotent }`. `decayPct` is rendered as a percentage with two decimal places (basis-point precision) and `slashAmt` reflects the amount routed to the slashing subsystem (zero when disabled). `newWeight` reports the post-penalty participation weight for observability.

## Appeals & remediation process

No dispute or appeals mechanism is currently implemented. There is no `potso_submitAppeal` RPC method or equivalent (confirmed: no "appeal" references anywhere in the Go source), no triage/hearing workflow, and no `potso.penalty.reversed`/`adjusted`/`refund` events. The evidence `type` field is hard-restricted to `DOWNTIME`, `EQUIVOCATION`, and `INVALID_BLOCK_PROPOSAL` with no appeal-flavored type or flag. An offender who believes evidence was filed in error currently has no on-chain or RPC-level recourse -- this is a known gap, not a documented process.

### Audit logging fields

All evidence and penalty actions feed into the audit log stream `potso.audit`. Each record contains:

| Field | Description |
| --- | --- |
| `eventType` | `evidence_submitted`, `evidence_rejected`, `penalty_applied`. |
| `hash` | Canonical evidence hash. |
| `offender` | Validator address. |
| `actor` | Reporter or governance signer responsible for the action. |
| `timestamp` | ISO8601 string with millisecond precision. |
| `decision` | Present for appeals: `approve`, `deny`, or `partial`. |
| `metadata` | JSON blob mirroring RPC payloads (redacted of secrets). |

Operators ingest this stream into retention storage with a minimum 365 day retention policy. The stream underpins compliance reporting and enables deterministic reconstruction of penalty history during audits.

