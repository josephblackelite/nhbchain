# Reputation Lifecycle

An attestation is identified by `reputation.AttestationID`, the Keccak-256 of `subject || Keccak256(lowercase(trim(skill))) || verifier` (`ComputeAttestationID`, `native/reputation/types.go`). The ID appears as `attestationId` (hex) in the events below. Only one attestation exists per `(subject, skill, verifier)` combination.

## Issuance

`Node.ReputationVerifySkill` (called from the `reputation_verifySkill` RPC, see [overview](overview.md)) checks that the verifier holds `ROLE_REPUTATION_VERIFIER`, otherwise returns `ErrReputationVerifierUnauthorized`. It then validates the record, and `Ledger.Put` stores it at `reputation/skill/<subject hex>/<skill digest hex>/<verifier hex>` with an index entry `reputation/attestation/<id hex>` that maps the ID back to the subject, skill and verifier (values are `KVPut` RLP records).

`Put` overwrites any earlier record for the same `(subject, skill, verifier)`, including a revoked one: re-issuing replaces the record with a fresh, non-revoked one.

Event `reputation.skillVerified`, attributes: `subject` and `verifier` (hex, no `0x`), `skill`, `issuedAt`, `expiresAt` (only if set), `attestationId`.

## Expiry

`Ledger.Get(subject, skill, verifier)` returns "not found" for an attestation whose `ExpiresAt` is set and `now >= ExpiresAt`, or which is revoked. The clock is the node's (`Node.currentTime()` in the node methods). No event is emitted at expiry.

## Revocation

`Node.ReputationRevokeSkill(verifier, attestationID, reason)` checks the verifier role, then `Ledger.Revoke`:

- unknown ID: `ErrAttestationNotFound`;
- the caller is not the address that issued it: `ErrRevocationUnauthorized`;
- already revoked: `ErrAttestationRevoked`;
- otherwise sets `RevokedAt`, `RevokedBy` and the trimmed `reason`, and the node appends `reputation.skillRevoked`.

Event `reputation.skillRevoked`, attributes: `attestationId`, `subject`, `verifier`, `skill`, `revokedAt`, and `reason` if non-empty.

This method is not reachable over RPC: no RPC handler or CLI command calls it.
