# Reputation Lifecycle

The reputation module issues attestations that capture a verifier's statement
about a subject's proficiency in a skill. Each attestation is identified by a
stable hash derived from the subject address, the normalized skill name and the
issuer. The attestation identifier is exposed via `reputation.AttestationID` and
is included on all emitted events so external indexers can follow lifecycle
transitions.

## Issuance

Verifiers call `Node.ReputationVerifySkill` to issue an attestation. The module
normalizes the skill label, validates the payload and persists the record using
an index keyed by `(subject, skill_hash, issuer)` for constant-time lookups.
Optional expirations must be strictly after the issue time; attestations with an
`expiresAt` in the past are rejected during validation.

## Expiry

Ledger lookups enforce expiration checks using the node's clock. Expired
attestations are treated as missing and are never returned through `Get`
operations. Consumers that cache attestations should respect the `expiresAt`
value to avoid presenting stale information.

## Revocation

Verifiers may revoke their own attestations by calling
`Node.ReputationRevokeSkill(attestationID, reason)`. Revocation marks the record
with a timestamp and optional justification, preventing it from being returned
on future reads. A `reputation.skillRevoked` audit event is emitted to provide a
traceable history for downstream systems.
