# Arbitration Governance Guide

This page covers how governance interacts with escrow arbitration, as
implemented in `native/escrow`, `core/state_transition.go` and
`rpc/modules/escrow.go`.

## Who can change an arbitration realm

An arbitration realm (`escrow.EscrowRealm`: `ID`, `Version`, `NextPolicyNonce`,
`CreatedAt`, `UpdatedAt`, arbitrator set, fee schedule, metadata) is created or
replaced by a signed transaction, not by a governance proposal payload:

- `TxTypeEscrowCreateRealm` / `TxTypeEscrowUpdateRealm`
  (`applyEscrowCreateRealm`, `applyEscrowUpdateRealm`,
  `core/state_transition.go`).
- The signer must hold the role `ROLE_ESCROW_REALM_ADMIN`
  (`RoleEscrowRealmAdmin`); otherwise the transaction fails with
  `escrowCreateRealm: unauthorized: caller lacks ROLE_ESCROW_REALM_ADMIN` or
  `escrowUpdateRealm: unauthorized: caller lacks ROLE_ESCROW_REALM_ADMIN`
  respectively.
- `nhb-cli escrow create-realm --key <keyfile> ...` builds and submits the
  create transaction (`cmd/nhb-cli/escrow_cmd.go`). The JSON payload fields are
  `id`, `threshold`, `scheme`, `members`, `feeBps`, `feeRecipient`, `scope`,
  `providerProfile`, `arbitrationFeeBps`, `feeRecipientBech32`
  (`decodeEscrowRealmPayload`).

Governance controls who holds that role. A `role.allowlist` proposal can grant
or revoke `ROLE_ESCROW_REALM_ADMIN` only if the role name is in the node's
`[governance] AllowedRoles` (the repository's root `config.toml` lists it).
The `role.allowlist` payload has only `role`, `address` and `memo`; it has no
realm field.

## Governed realm bounds

The escrow engine reads three param-store keys when it validates a realm
(`native/escrow/engine.go`):

| Key | Validation | Value when unset |
| --- | --- | --- |
| `escrow.realm.MinThreshold` | integer `1`-`100` | `1` |
| `escrow.realm.MaxThreshold` | integer `1`-`100` | `10` |
| `escrow.realm.AllowedSchemes` | non-empty array (or string) of `single`, `committee` or a numeric scheme id | `single` and `committee` |

A `param.update` proposal can set them only if they are in `AllowedParams`; they
are not in the default list (see [params](./params.md)). The engine rejects a
configuration where the minimum exceeds the maximum. The threshold values are
read through `nativecommon.ParamDecimal`, so a bare and a quoted number are read
the same way. The escrow and trade engines stamp `CreatedAt`, `UpdatedAt`,
`FrozenAt` and their deadline checks with the block timestamp
(`configureTradeEngine`, `core/state_transition.go`), not the wall clock.

## `ROLE_ARBITRATOR`

`ROLE_ARBITRATOR` is checked by `Node.P2PResolve` (`core/node.go`), the
resolution path for peer-to-peer trade disputes: a caller without the role gets
`trade: caller lacks arbitrator role`. Granting it through `role.allowlist`
requires it to be in `AllowedRoles`.

## Frozen policy inside an escrow

When an escrow is created with a realm id, `Engine.Create` copies the realm's
arbitrator policy into the escrow record as `FrozenArb`
(`native/escrow/types.go`): `RealmID`, `RealmVersion`, `PolicyNonce`, `Scheme`,
`Threshold`, `Members`, `FrozenAt`, fee schedule and metadata. The escrow keeps
that copy, so a later realm update does not change an existing escrow's policy.

## RPC methods

| Method | Use |
| --- | --- |
| `escrow_getRealm` | Current realm definition. |
| `escrow_getSnapshot` | The escrow plus `frozenPolicy` (`realmId`, `realmVersion`, `policyNonce`, `scheme`, `threshold`, `members`, `frozenAt`, `metadata`), when the escrow has one. |
| `escrow_listEvents` | Escrow events in order. |

Realm events are `escrow.realm.created` and `escrow.realm.updated`; dispute
events include `escrow.disputed`, `escrow.resolved`, `escrow.trade.disputed`
and `escrow.trade.resolved` (`native/escrow/events.go`).
