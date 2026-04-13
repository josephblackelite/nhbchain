# POTSO Evidence Intake

Phase 3A introduces a canonical intake flow for POTSO misbehaviour evidence. This layer validates authenticity, enforces replay protection, and persists accepted records so that subsequent penalty logic can consume a deduplicated feed.

## Evidence payload schema

Evidence submissions must include the following fields:

| Field | Type | Notes |
| --- | --- | --- |
| `type` | string | One of `DOWNTIME`, `EQUIVOCATION`, `INVALID_BLOCK_PROPOSAL`. |
| `offender` | string | NHB Bech32 address of the validator being accused. |
| `heights` | array<uint64> | Block heights relevant to the accusation. Heights must be in ascending order. |
| `details` | JSON | Free-form, reporter-controlled data. The raw bytes are hashed for dedupe. |
| `reporter` | string | NHB Bech32 address of the reporter. |
| `reporterSig` | hex | 65-byte secp256k1 signature authenticating the payload. |
| `timestamp` | int64 | Reporter clock in UNIX seconds; embedded into the signing digest. |

## Canonical hash & replay guard

Every payload is mapped to a canonical hash:

```
blake3(type || offender || len(heights) || heights || details)
```

`type` is upper-cased and ASCII encoded, addresses are raw 20-byte values, and heights are encoded as big-endian 64-bit integers prefixed by the list length. This hash is stable across reporters and serves two purposes:

* Replay protection – any submission with a previously seen hash is treated as idempotent and no new record is written.
* Query key – `potso_getEvidence` resolves records by canonical hash.

The signature domain uses the canonical hash and timestamp: reporters sign the SHA-256 digest of `"potso_evidence|<hash>|<timestamp>"`.

## Authenticity checks

The verifier enforces:

* Known evidence type.
* Non-zero offender and reporter addresses.
* Ascending `heights` list.
* Heights not in the future relative to the node's tip.
* Heights within the rolling window (`DefaultMaxAgeBlocks = 8640`).
* Heights that actually exist in the canonical chain.
* Valid 65-byte secp256k1 signature matching the reporter.

Failures emit `potso.evidence.rejected` events with the reporter address and a machine-readable reason such as `invalid_signature` or `expired`.

## Persistence & queries

Accepted submissions are stored with their canonical hash, full payload, and the UTC arrival timestamp. Duplicate submissions surface `status = "idempotent"` and return the stored record.

RPC surfaces three endpoints under the POTSO namespace:

* `potso_submitEvidence(EvidencePayload) -> { hash, status }`
* `potso_getEvidence(hash) -> EvidenceRecord`
* `potso_listEvidence(filters?) -> { records, nextOffset? }`

Filters support `offender`, `type`, `fromHeight`, `toHeight`, and pagination via `page: { offset, limit }`. Results expose raw `details` bytes exactly as submitted, the reporter signature, and the server-side `receivedAt` timestamp.

## Events

Two new topics are emitted:

* `potso.evidence.accepted { hash, type, offender, height, reporter }` for new records (the smallest referenced height is published).
* `potso.evidence.rejected { reason, reporter }` when validation fails.

Downstream consumers can subscribe to these to trigger dashboards, alerting, or follow-on enforcement once penalty logic is wired up.

## Penalty math & idempotency

Phase 3B introduces a deterministic penalty engine that maps accepted evidence to participation weight decay and optional token slashing. The rules are table-driven per evidence type:

| Evidence type | Severity | Weight decay | Slash | Cooldown |
| --- | --- | --- | --- | --- |
| `EQUIVOCATION` | Critical | `max(θ_eq × baseWeight, minDecay)` | Optional `S_eq` basis points of base weight (feature-gated) | 7 epochs |
| `DOWNTIME` | Medium | Ladder: `θ_dt(N)` for `N` missed epochs (defaults: 2%, 5%, 10%) | None | 1 epoch |
| `INVALID_BLOCK_PROPOSAL` | High | Fixed percentage (default 3%) of current weight | None | 1 epoch |

Decay percentages are expressed in basis points and applied against the offender's participation weight. Results are clamped between configured floor and ceiling bounds to prevent negative or runaway values. When slashing is disabled, any computed slash amount is ignored but still surfaced to telemetry.

Every application is idempotent: the pair `{evidenceHash, offender}` is recorded before mutating state. Replaying the same evidence produces no additional weight change and emits an event flagged `idempotent=true` so operators can distinguish duplicate submissions from fresh penalties.

### Penalty events

Successful executions emit `potso.penalty.applied { hash, type, offender, decayPct, slashAmt, newWeight, block, idempotent }`. `decayPct` is rendered as a percentage with two decimal places (basis-point precision) and `slashAmt` reflects the amount routed to the slashing subsystem (zero when disabled). `newWeight` reports the post-penalty participation weight for observability.

## Appeals & remediation process

POTSO recognises a structured appeals flow for validators who believe evidence was filed in error:

1. **Appeal intake** – Offenders submit an appeal via `potso_submitAppeal` with the disputed `evidenceHash`, a narrative, and supporting documents (IPFS hashes or URLs). Appeals must land within five epochs of the penalty block.
2. **Triage** – The compliance rotation reviews the evidence bundle and correlates it with consensus telemetry. During triage the penalty weight decay continues to apply; only slashing transfers are held in escrow.
3. **Hearing** – A quorum (≥3) of governance signers review the triage packet inside the appeals dashboard. The decision is captured as `approve`, `deny`, or `partial` (for reduced penalties).
4. **Resolution** – Approved appeals create `potso.penalty.reversed` events and refund any held slash amount. Partial resolutions emit both `potso.penalty.adjusted` and `potso.penalty.refund` events with the adjusted decay and slash.

Appeals are idempotent by `{evidenceHash, offender}`. Replays update the appeal record without changing timestamps so downstream systems can safely retry submissions.

### Audit logging fields

All evidence and penalty actions feed into the audit log stream `potso.audit`. Each record contains:

| Field | Description |
| --- | --- |
| `eventType` | `evidence_submitted`, `evidence_rejected`, `penalty_applied`, `appeal_filed`, `appeal_decided`, `penalty_reversed`. |
| `hash` | Canonical evidence hash. |
| `offender` | Validator address. |
| `actor` | Reporter or governance signer responsible for the action. |
| `timestamp` | ISO8601 string with millisecond precision. |
| `decision` | Present for appeals: `approve`, `deny`, or `partial`. |
| `metadata` | JSON blob mirroring RPC payloads (redacted of secrets). |

Operators ingest this stream into retention storage with a minimum 365 day retention policy. The stream underpins compliance reporting and enables deterministic reconstruction of penalty history during audits.

