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

