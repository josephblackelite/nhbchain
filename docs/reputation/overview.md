# Reputation service overview

The reputation module stores skill attestations: a verifier records that a subject has a named skill. Code: `native/reputation`, `Node.ReputationVerifySkill` / `ReputationRevokeSkill` in `core/node.go`, `rpc/reputation_handlers.go`.

## Status: no live entry point

A running node cannot create, read or revoke attestations:

- `reputation_verifySkill`, the only reputation RPC method, is still routed but always answers HTTP 410 with JSON-RPC code `-32060` and a message saying the method is disabled (`handleReputationVerifySkill`, `reputationRPCDisabledMessage` in `rpc/reputation_handlers.go`; `codeMethodDisabled` in `rpc/http.go`). It performs no authentication and reads no parameters. The code comment gives the reason: the old handler wrote the attestation to the live state of the one validator that handled the call, outside block execution, and took the verifier address from the request body without proof that the caller held the verifier key.
- There is no reputation transaction type (`core/types/txtypes_registry.go` lists none) and no `nhb-cli` command.
- There is no RPC method to read, list or revoke attestations. `Node.ReputationRevokeSkill` exists in Go but nothing calls it.
- `Node.ReputationVerifySkill` is kept for its tests. Its comment says it must not be exposed again, because a write made there becomes part of one validator's next proposed block only.

The rest of this section and [lifecycle](lifecycle.md) describe what the in-process module does, for anyone reading or testing the code. None of it is reachable on a running node.

## Data model

- `SkillVerification` (`native/reputation/types.go`): `Subject`, `Skill`, `Verifier` (20-byte addresses and a string), `IssuedAt`, `ExpiresAt` (Unix seconds; `ExpiresAt <= 0` means no expiry).
- `Validate` requires: non-empty (trimmed) skill, non-zero subject and verifier, positive `IssuedAt`, and `ExpiresAt > IssuedAt` when `ExpiresAt` is set. Skill names compare case-insensitively (the key and the attestation ID use the lower-cased, trimmed name).
- `Node.ReputationVerifySkill` sets `IssuedAt` to the node's current time, so an `expiresAt` must be later than that.

## In-process issuance flow

1. `Node.ReputationVerifySkill(verifier, subject, skill, expiresAt)` trims the skill (empty: "reputation: skill required"), builds the record with `IssuedAt` = node time and calls `Validate`.
2. It checks that `verifier` holds the role `ROLE_REPUTATION_VERIFIER` (`roleReputationVerifier`, `core/node.go`). Otherwise it returns `ErrReputationVerifierUnauthorized` ("reputation: caller lacks verifier role").
3. The record is stored and `reputation.skillVerified` is appended to the node's events. See [lifecycle](lifecycle.md).

The verifier address is a plain argument: nothing in this function proves the caller holds the verifier's key. The role check is the only authorization.

## Granting the verifier role

The role name is `ROLE_REPUTATION_VERIFIER`. Ways to give it to an address:

- The `roles` map in the genesis file (role name to list of addresses; `core/genesis/spec.go`).
- A `role.allowlist` governance proposal ([governance overview](../governance/overview.md#proposal-kinds)). The proposal is rejected unless the role is listed in the node's `[governance] AllowedRoles`. The `AllowedRoles` list in the repository's `config.toml` does not include `ROLE_REPUTATION_VERIFIER`, and `MINTER_ZNHB` can never be granted this way (`parseRoleAllowlistPayload`, `native/governance/engine.go`).

## Disputes

There is no dispute tooling in the module.
