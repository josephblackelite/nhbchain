# Reputation service overview

The reputation module introduces a minimal skill verification primitive. Verifiers attest that a subject possesses a specific capability. The current release focuses on surfacing deterministic events while the on-chain role checks remain permissive stubs.

## Verification flow

1. A wallet with verifier privileges calls `reputation_verifySkill`.
2. The RPC validates addresses, normalises skill strings and forwards the request to the core node.
3. The node will, in a future update, enforce role membership, persist the verification and emit `reputation.skillVerified`.

The RPC returns the canonical payload comprising the subject, verifier, skill, issuance timestamp and optional expiry.

## Responsibilities of verifiers

* Maintain an auditable log of evidence backing each verification.
* Ensure expiring attestations are revisited and either renewed or revoked off-chain.
* Coordinate with governance to define what constitutes acceptable proof for a skill category.

## Disputes

The module does not yet ship automated dispute tooling. Consumers should subscribe to the `reputation.skillVerified` event stream and build application-specific review workflows. When the fully stateful module lands it will include revocation semantics and anchoring into escrow dispute committees.
