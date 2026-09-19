# Reputation service overview

The reputation module stores skill attestations: a verifier records that a subject has a named skill. Code: `native/reputation`, `Node.ReputationVerifySkill` / `ReputationRevokeSkill` in `core/node.go`, `rpc/reputation_handlers.go`.

The only reputation RPC method is `reputation_verifySkill`. There is no RPC method to read, list or revoke attestations (`Node.ReputationRevokeSkill` exists in Go but nothing in the repository calls it). Reads exist only as `Ledger.Get` inside the module.

## `reputation_verifySkill`

Requires authentication: a JWT bearer token or verified client certificate (HTTP 401, code `-32001` otherwise). Exactly one parameter object:

```json
{
  "verifier": "nhb1...",
  "subject": "nhb1...",
  "skill": "solidity",
  "expiresAt": 1893456000
}
```

- `verifier`, `subject`: Bech32 addresses.
- `skill`: required; trimmed. Skill names compare case-insensitively.
- `expiresAt`: optional Unix seconds. A value `<= 0` or omitted means no expiry. It must be later than the issue time, which is the node's current time.

The RPC does not verify a signature over the request: `verifier` is taken from the parameter. Authorization is the role check below.

### Flow

1. The RPC validates the addresses and skill.
2. `Node.ReputationVerifySkill` builds the record with `IssuedAt` = node time and calls `Validate`: non-empty skill, non-zero subject and verifier, positive `IssuedAt`, and `ExpiresAt > IssuedAt` if set.
3. It checks that `verifier` holds the role `ROLE_REPUTATION_VERIFIER` (`roleReputationVerifier`, `core/node.go`). Otherwise it returns `ErrReputationVerifierUnauthorized` ("reputation: caller lacks verifier role").
4. The record is stored and `reputation.skillVerified` is appended to the node's events.

Response: `{"verifier", "subject", "skill", "issuedAt", "expiresAt"?}` (`expiresAt` omitted when there is none).

### Errors

Errors raised by the RPC layer itself (before the node is called):

| Condition | HTTP | Code | `message` | `data` |
| --- | --- | --- | --- | --- |
| Not exactly one parameter, bad JSON, invalid Bech32 address, empty skill | 400 | `-32602` | `invalid_params` | detail such as `skill required` or the Bech32 error |

Errors returned by `Node.ReputationVerifySkill` go through `writeReputationError`, which classifies by substring of the error text:

| Error text contains | HTTP | Code | `message` | `data` |
| --- | --- | --- | --- | --- |
| `invalid` | 400 | `-32602` | `invalid_params` | error text |
| `unauthorized`, or is `ErrReputationVerifierUnauthorized` | 403 | `-32001` | `forbidden` | error text (`reputation: caller lacks verifier role`) |
| anything else | 500 | `-32000` | `internal_error` | error text |

The validation errors in `SkillVerification.Validate` ("expiresAt must be after issuedAt", "subject required", "verifier required", ...) do not contain "invalid", so they are returned as HTTP 500 `internal_error`.

## Granting the verifier role

The role name is `ROLE_REPUTATION_VERIFIER`. Ways to give it to an address:

- The `roles` map in the genesis file (role name to list of addresses; `core/genesis/spec.go`).
- A `role.allowlist` governance proposal ([governance overview](../governance/overview.md#supported-proposal-kinds)). The proposal is rejected unless the role is listed in the node's `[governance] AllowedRoles`. The `AllowedRoles` list in the repository's `config.toml` does not include `ROLE_REPUTATION_VERIFIER`, and `MINTER_ZNHB` can never be granted this way.

## Verifier responsibilities

Off-chain policy, not enforced by code: keep evidence for each attestation, re-issue or revoke expiring ones, and agree acceptable proof per skill.

## Disputes

There is no dispute tooling in the module. Consumers can watch the `reputation.skillVerified` and `reputation.skillRevoked` events and build their own review process.
