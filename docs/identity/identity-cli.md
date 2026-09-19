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

## Commands that do not work

These `nhb-cli id` subcommands exist but call JSON-RPC methods that are disabled on the node (HTTP 410, error code `-32060`), so they fail:

| Command | Flags | RPC method |
| --- | --- | --- |
| `id set-alias` | `--addr`, `--alias` | `identity_setAlias` |
| `id set-avatar` | `--addr`, `--avatar` | `identity_setAvatar` |
| `id add-address` | `--owner`, `--alias`, `--addr` | `identity_addAddress` |
| `id remove-address` | `--owner`, `--alias`, `--addr` | `identity_removeAddress` |
| `id set-primary` | `--owner`, `--alias`, `--addr` | `identity_setPrimary` |
| `id rename` | `--owner`, `--alias`, `--new-alias` | `identity_rename` |
| `id create-claimable` | `--payer`, `--recipient`, `--token` (default `NHB`), `--amount`, `--deadline` | `identity_createClaimable` |
| `id claim` | `--id`, `--payee`, `--preimage` | `identity_claim` |

Use `claim-username` to register a username.

## Notes

* Output of the `id` commands is the raw JSON result; RPC errors print as `RPC error <code>: <message>`.
* The write RPCs (when they were enabled) were called with the authentication token from `NHB_RPC_TOKEN`; `resolve` and `reverse` need no token.
