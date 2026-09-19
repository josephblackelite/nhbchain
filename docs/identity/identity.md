# Identity and Username Directory

The identity module maps human-readable aliases (usernames) to account addresses. Code: `core/identity/alias.go` (alias rules), `core/state/manager.go` (`Identity*` storage), `core/state_transition.go` (`applyRegisterIdentity`), `rpc/identity_handlers.go`, `core/claimable`, `services/identity-gateway`.

## What is live

| Capability | Status |
| --- | --- |
| Claim a username | **Live**: `TxTypeRegisterIdentity` (`0x02`), `nhb-cli claim-username <username> <key_file>`. |
| Resolve an alias, reverse-lookup an address | **Live**: `identity_resolve`, `identity_reverse` (public, no authentication). |
| Rename, link/unlink addresses, set primary address, set avatar | **Not available**: `identity_setAlias`, `identity_setAvatar`, `identity_addAddress`, `identity_removeAddress`, `identity_setPrimary`, `identity_rename` are disabled (HTTP 410, code `-32060`) and there is no transaction type for these operations. |
| Claimables (pay a recipient who has no address yet) | **Not available for writes**: `identity_createClaimable`, `identity_claim`, `claimable_create`, `claimable_claim`, `claimable_cancel` are disabled, and there is no claimable transaction type. `claimable_get` (read) is live. |

The disabled methods used to change the receiving node's state outside block execution (`identityRPCDisabledMessage`, `claimableRPCDisabledMessage`).

## Terminology

| Term | Description |
| --- | --- |
| **Alias** | The username string, globally unique. |
| **Alias ID** | `keccak256(normalized alias)` (`identity.DeriveAliasID`), 32 bytes, shown as `0x` plus hex as `aliasId`. |
| **Primary address** | `AliasRecord.Primary`: the address returned by `identity_resolve` as `primary`. A newly registered alias has the registering address as primary and as its only address. |
| **Owner** | `AliasRecord.Owner`: the registering address; the alias can only be re-registered by it. Not part of the `identity_resolve` result. |
| **Avatar reference** | `AliasRecord.AvatarRef`, a string starting with `https://` or `blob://`. |

## Normalization and uniqueness (`identity.NormalizeAlias`)

* The alias is trimmed and lower-cased (`strings.ToLower`); there is no other Unicode normalization.
* After normalization it must be 3 to 32 bytes long and match `^[a-z0-9._-]+$`.
* Uniqueness is on the normalized form. Registration fails with `identity: alias already registered` if another owner holds it.

## Alias record (`identity.AliasRecord`)

```go
type AliasRecord struct {
  Alias     string
  Owner     [20]byte
  Primary   [20]byte
  Addresses [][20]byte
  AvatarRef string
  CreatedAt int64
  UpdatedAt int64
}
```

Storage keys (`core/state/manager.go`): `identity/alias/<alias>` (the record), `identity/alias-id/<aliasId>` (alias string) and a reverse entry mapping the address to its alias.

## Claiming a username (`applyRegisterIdentity`)

* Transaction: type `TxTypeRegisterIdentity` = `0x02`, `tx.Data` = the alias as raw UTF-8 bytes, no value.
* Checks: the alias normalizes successfully; it is not already in the node's username index (`username '<alias>': ...taken`); the sender's account does not already have a username (`account already has username`); the alias record is not owned by another address.
* Effects: writes the alias record with the sender as owner and primary and the block time as `CreatedAt`/`UpdatedAt`, sets `Account.Username`, increments the sender's nonce, records engagement activity. It emits **no** identity event.
* CLI: `nhb-cli claim-username <username> <key_file>` builds and sends this transaction.

## Events

`core/events/identity.go` defines `identity.alias.set`, `identity.alias.renamed`, `identity.alias.avatarUpdated`, `identity.alias.addressLinked`, `identity.alias.addressRemoved` and `identity.alias.primaryUpdated`. They are emitted only by the `Node.Identity*` methods behind the disabled RPCs. Claiming a username through `TxTypeRegisterIdentity` does not emit them. Claimable events (`claimable.created`, `claimable.claimed`, `claimable.cancelled`, `claimable.expired`, `core/events/claimable.go`) are likewise only produced by the disabled claimable paths.

## Claimables (code present, writes disabled)

`core/claimable` defines a hash-locked payment held for a recipient:

```go
type Claimable struct {
  ID, HashLock, RecipientHint [32]byte
  Payer          [20]byte
  Token          string
  Amount         *big.Int
  RecipientKind  RecipientKind   // 0 = none/opaque secret, 1 = alias-derived
  Deadline, CreatedAt, ExpiresAt int64
  Nonce          uint64
  ChainID        string
  Status         ClaimStatus     // 0 init, 1 claimed, 2 cancelled, 3 expired
}
```

The claim rules implemented in the node code (used by the disabled methods) are described in [`pay-by-email.md`](./pay-by-email.md). `claimable_get` returns a record; see [`identity-api.md`](./identity-api.md).

## Off-chain email directory

`services/identity-gateway` verifies email addresses and stores salted hashes off-chain. See [`identity-gateway.md`](./identity-gateway.md). Nothing on-chain stores an email or an email hash except the `RecipientHint` of a claimable.

## Related documents

* [Identity JSON-RPC reference](./identity-api.md)
* [Identity CLI](./identity-cli.md)
* [Identity gateway REST API](./identity-gateway.md)
* [Pay by username](./pay-by-username.md) and [pay by email](./pay-by-email.md)
* [Avatar rules](./avatars.md)
* [Events and audit](./audit.md)
* [Security notes](./identity-security-compliance.md)
