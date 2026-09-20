# `nhb-cli` Identity Commands

`nhb-cli` (`cmd/nhb-cli/main.go`, `cmd/nhb-cli/identity_cmd.go`) has an `id` command group and a top-level `claim-username` command. The RPC endpoint defaults to `http://localhost:8080`, or the `RPC_URL` environment variable, or the `--rpc <url>` flag. There are no `--node` or `--chain-id` flags.

## Working commands

### Claim a username

```bash
nhb-cli claim-username <username> <key_file>
```

Builds and signs a `TxTypeRegisterIdentity` transaction (type `0x02`, `tx.Data` = the username, gas limit 50000, gas price 1) from the key file and submits it. The username is normalized and validated on-chain (3 to 32 characters of `a-z 0-9 . _ -`). See [`identity.md`](./identity.md).

### Resolve an alias

```bash
nhb-cli id resolve --alias frankrocks
```

Calls `identity_resolve` and prints the raw JSON result (`alias`, `aliasId`, `primary`, `addresses`, `avatarRef` if set, `createdAt`, `updatedAt`).

### Reverse lookup

```bash
nhb-cli id reverse --addr nhb1...
```

Calls `identity_reverse` and prints `{"alias": ..., "aliasId": ...}`.

## Retired commands

These `nhb-cli id` subcommands are retired. The CLI does not contact the node for them: each prints that the command is retired because the node no longer serves the method (HTTP 410), and exits with status 1 (`identityRetiredMethods`, `cmd/nhb-cli/identity_cmd.go`; `reportRetired`, `cmd/nhb-cli/retired_cmd.go`).

| Command | Node method it used |
| --- | --- |
| `id set-alias` | `identity_setAlias` |
| `id set-avatar` | `identity_setAvatar` |
| `id add-address` | `identity_addAddress` |
| `id remove-address` | `identity_removeAddress` |
| `id set-primary` | `identity_setPrimary` |
| `id rename` | `identity_rename` |
| `id create-claimable` | `identity_createClaimable` |
| `id claim` | `identity_claim` |

Use `claim-username` to register a username.

## Claimable commands

`nhb-cli claimable get --id <0x...>` calls `claimable_get` and prints the raw JSON result. `claimable create`, `claimable claim` and `claimable cancel` are retired in the same way as the `id` commands above (`claimableRetiredMethods`, `cmd/nhb-cli/claimable_cmd.go`).

## Notes

* Output of the `id` commands is the raw JSON result; RPC errors print as `RPC error <code>: <message>`.
* Every failing command, including usage errors, exits non-zero. `claim-username` sends a transaction, so it needs `NHB_RPC_TOKEN` (`nhb-cli rpc-token` prints one when run on the node host); `id resolve`, `id reverse` and `claimable get` need no token.
