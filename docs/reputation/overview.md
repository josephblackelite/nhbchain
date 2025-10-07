# Reputation service overview

The reputation module introduces a minimal skill verification primitive. Verifiers attest that a subject possesses a specific capability. The current release now enforces verifier authorization within the node; calls from wallets that do not hold the `roleReputationVerifier` assignment fail before any state transition.

## Verification flow

1. A wallet with verifier privileges calls `reputation_verifySkill`.
2. The RPC validates addresses, normalises skill strings and forwards the request to the core node.
3. The node enforces role membership, persists the verification and emits `reputation.skillVerified`.

The RPC returns the canonical payload comprising the subject, verifier, skill, issuance timestamp and optional expiry.

### Error semantics

Validation failures return `codeInvalidParams` (`-32602`) with the specific guard encoded in the `message`/`data` pair (for example `"invalid_params"` + `"invalid bech32 string"` or `"skill required"`). Calls from wallets that lack the verifier role surface `codeUnauthorized` with HTTP `403`, while infrastructure failures fall back to `codeServerError` (`-32000`).

### Authorization

`Node.ReputationVerifySkill` checks that the caller holds `roleReputationVerifier` and returns `ErrReputationVerifierUnauthorized` when the role is missing. The RPC layer surfaces the error as `codeUnauthorized` (`403`) with the canonical message and `data` payload so client SDKs can present actionable guidance. Follow the [role allow-list governance workflow](../governance/overview.md#supported-proposal-kinds) to grant or revoke verifier privileges; operators running private networks can edit the genesis role map or submit equivalent `role.allowlist` proposals during rollout.

### Migration considerations

Earlier previews only emitted warnings when the caller lacked the verifier role. Integrations that relied on that soft enforcement must now ensure every attesting wallet holds `roleReputationVerifier` before submitting RPC calls. Update automated test fixtures, back-office runbooks, and multisig or KMS policies to cover the stricter requirement; failing to do so will result in `ErrReputationVerifierUnauthorized` responses and no attestation being recorded.

## Responsibilities of verifiers

* Maintain an auditable log of evidence backing each verification.
* Ensure expiring attestations are revisited and either renewed or revoked off-chain.
* Coordinate with governance to define what constitutes acceptable proof for a skill category.

## Disputes

The module does not yet ship automated dispute tooling. Consumers should subscribe to the `reputation.skillVerified` event stream and build application-specific review workflows. When the fully stateful module lands it will include revocation semantics and anchoring into escrow dispute committees.
