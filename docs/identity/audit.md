# Identity Events and Audit Data

What the identity and claimable code records or emits. Code: `core/events/identity.go`, `core/events/claimable.go`, `core/state_transition.go`, `core/node.go`, `services/identity-gateway`.

## Events defined

| Event | Attributes | Emitted by |
| --- | --- | --- |
| `identity.alias.set` | `alias`, `address` | `Node.IdentitySetAlias` (behind the disabled `identity_setAlias`) |
| `identity.alias.renamed` | `old`, `new`, `address` | `Node.IdentitySetAlias` / `Node.IdentityRename` (disabled paths) |
| `identity.alias.avatarUpdated` | `alias`, `address`, `avatarRef` | `Node.IdentitySetAvatar` (disabled path) |
| `identity.alias.addressLinked` | `alias`, `address` | `Node.IdentityAddAddress` (disabled path) |
| `identity.alias.addressRemoved` | `alias`, `address` | `Node.IdentityRemoveAddress` (disabled path) |
| `identity.alias.primaryUpdated` | `alias`, `address` | `Node.IdentitySetPrimary` (disabled path) |
| `claimable.created` | `id`, `payer`, `token`, `amount`, `deadline`, `createdAt`, `recipientHint` | `Node.ClaimableCreate` / `IdentityCreateClaimable` (disabled paths) |
| `claimable.claimed` | `id`, `payer`, `payee`, `token`, `amount`, `recipientHint` | `Node.ClaimableClaim` / `IdentityClaim` (disabled paths) |
| `claimable.cancelled`, `claimable.expired` | `id`, `payer`, `token`, `amount` | `Node.ClaimableCancel` / `ClaimableExpire` (disabled paths) |

Addresses in these events are bech32 (`nhb1...`); `id` and `recipientHint` are hex without `0x`.

The one live way to create an alias, `TxTypeRegisterIdentity` (`applyRegisterIdentity`), emits **no identity event**. Its trace is the transaction itself, the alias record (`identity/alias/<alias>`) with `CreatedAt`/`UpdatedAt`, and `Account.Username` on the sender's account.

## Where events go

Events emitted during block execution are appended to the block's event list (`StateProcessor.AppendEvent`). The node exposes recent events of the escrow module through `escrow_listEvents`; there is no identity-specific event RPC.

## Identity gateway records

The gateway (`services/identity-gateway`) keeps a BoltDB file with per-email records (salted hash, code digest and expiry, register attempt timestamps, verification time, bind token digest and expiry, alias bindings), an alias-to-email-hash index, and idempotency records. It has no audit-log endpoint and no configured retention beyond the idempotency TTL (default 24 hours) and the verification/bind token lifetimes (10 minutes and 15 minutes by default). See [`identity-gateway.md`](./identity-gateway.md).
