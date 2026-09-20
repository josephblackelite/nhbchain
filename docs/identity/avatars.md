# Avatar References

An alias record has an optional `AvatarRef` string. The chain stores only the string; it does not host, fetch or validate the media. Code: `core/identity/alias.go` (`NormalizeAvatarRef`), `core/state/manager.go` (`IdentitySetAvatar`), `rpc/identity_handlers.go`.

## Validation rules (`NormalizeAvatarRef`)

* Trimmed, non-empty.
* At most 512 bytes (`avatarRefMaxBytes`, compared with `len` of the trimmed string).
* Must start with `https://` or `blob://` (case-insensitive prefix check). Nothing else about the value is checked: not the host, the CID, the MIME type or the size of the referenced file.
* Invalid values fail with `identity: invalid avatar reference`.

`identity_resolve` returns the stored value as `avatarRef`, omitted when empty.

## Setting an avatar

The only interface that sets an avatar is `identity_setAvatar`, which is **disabled** (HTTP 410, code `-32060`); `nhb-cli id set-avatar` is retired and exits non-zero without contacting the node. No transaction type sets an avatar and the identity gateway has no avatar endpoint. New aliases registered with `TxTypeRegisterIdentity` therefore have no avatar. See [`identity.md`](./identity.md).

## Client guidance

Anything beyond the rules above is a client decision. If a wallet displays avatars it must fetch and validate the referenced content itself; the node provides no guarantee about it.
