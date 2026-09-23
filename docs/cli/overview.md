# `nhb-cli` overview

`nhb-cli` (source: `cmd/nhb-cli/`) is the command-line client for a node's
JSON-RPC endpoint. This page lists the commands the binary dispatches and the
settings they share. Per-command pages: [send](./send.md),
[stake](./stake.md), [staking](./staking.md), [keystore](./keystore.md).

## Settings shared by all commands

- **RPC endpoint.** `--rpc <url>` (or `--rpc=<url>`), which is stripped from the
  argument list wherever it appears; otherwise the `RPC_URL` environment
  variable; otherwise `http://localhost:8080`.
- **RPC token.** `NHB_RPC_TOKEN`. Any command that submits a transaction, and any
  call to a privileged RPC method, sends it as `Authorization: Bearer <token>`
  and fails with "privileged RPC call requires NHB_RPC_TOKEN to be set" when it
  is empty. Commands that need it: every transaction-submitting command,
  `stake position`, `stake preview`, `potso stake info` and `swap voucher ...`.
  Balance lookups (`nhb_getBalance`), the `loyalty-*` reads, `id resolve|reverse`,
  `escrow get`, `claimable get`, `p2p get`, `gov show|list`, `fees status`
  and the `subscriptions` lookups are sent without a token. (`doRPCRequest` in
  `cmd/nhb-cli/main.go`; the `requireAuth` argument at each call site.) To get a
  token on the node host, use [`rpc-token`](#tokens) below.
- **Key files.** A `<key_file>` argument is either a raw private-key file, as
  written by `generate-key`, or a JSON V3 keystore whose passphrase is in
  `NHB_KEYSTORE_PASSPHRASE` (`loadPrivateKey` in `cmd/nhb-cli/main.go`). A file
  that contains the old placeholder key material is refused.

## Top-level commands

Key and account:

- `generate-key [--force]` -- writes a new key to `wallet.key` in the current
  directory (mode `0600`) and prints its address. It never overwrites an existing
  `wallet.key`: it exits `1` and leaves the file untouched. With `--force` it
  first copies the existing file to `wallet.key.bak-<UTC time>` (created
  exclusively, mode `0600`) and replaces the key only after that copy is on disk;
  if the copy fails nothing is changed. Any other argument is an error
  (`generateKey` in `cmd/nhb-cli/main.go`).
- `address <key_file>` -- prints the address for a key file.
- `balance <address>` -- prints username, NHB and ZapNHB balances, stake, locked
  stake, delegation, validator registration, pending unbonds and nonce.
- `claim-username <username> <key_file>` -- sends `TxTypeRegisterIdentity`
  (`0x02`) with the username as data (gas limit `50000`).
- `keystore import --out <path>` -- see [keystore](./keystore.md).
- `help`, `-h`, `--help` -- print the usage text and exit `0`. Running with no
  command prints the usage text and exits `1`.

<a id="tokens"></a>
Tokens:

- `rpc-token [--secret-stdin] [--ttl <duration>] [--issuer <name>] [--audience <a,b>]`
  -- prints a bearer token, signed HS256 with the node's RPC JWT secret, for
  `NHB_RPC_TOKEN`. Run it on the node host. The secret is read from the
  `NHB_RPC_JWT_SECRET` environment variable, or from standard input with
  `--secret-stdin`; it is never a command-line argument. Defaults: `--ttl 10m`
  (must be greater than zero and at most 24h), `--issuer nhb-rpc`,
  `--audience wallets`; the issuer and audience must match the node's `[RPCJWT]`
  `Issuer` and `Audience`. It takes no positional arguments and prints only the
  token, so `export NHB_RPC_TOKEN="$(nhb-cli rpc-token)"` works
  (`cmd/nhb-cli/rpc_token.go`).

Transfers:

- `send-nhb`, `send-znhb` -- see [send](./send.md).

Staking and validators:

- `stake ...`, `un-stake <amount> <key_file>`, `register-validator <amount>
  <key_file>`, `deregister-validator <key_file>`, `set-reward-beneficiary
  <address|""> <key_file>` -- see [staking](./staking.md) and [stake](./stake.md).
- `heartbeat <key_file>` -- sends `TxTypeHeartbeat` (`0x08`) with zero value.

Loyalty (each `loyalty-*` command is its own top-level command):
`loyalty-create-business`, `loyalty-set-paymaster`, `loyalty-add-merchant`,
`loyalty-remove-merchant`, `loyalty-create-program`, `loyalty-update-program`,
`loyalty-pause-program`, `loyalty-resume-program`, `loyalty-get-business`,
`loyalty-list-businesses`, `loyalty-list-programs`, `loyalty-program-stats`,
`loyalty-user-daily`, `loyalty-paymaster-balance`, `loyalty-resolve-username`,
`loyalty-user-qr`. Running one with too few arguments prints its usage line.

Command groups (each has its own subcommands, implemented in the named file):

| Group | Subcommands | File |
| --- | --- | --- |
| `id` | `resolve --alias <alias>`, `reverse --addr <address>`. Retired: `set-alias`, `set-avatar`, `add-address`, `remove-address`, `set-primary`, `rename`, `create-claimable`, `claim` | `identity_cmd.go` |
| `escrow` | `create`, `get`, `fund`, `release`, `refund`, `expire`, `dispute`, `create-realm` (signed transactions except `get`), and `resolve`, which only prints that resolution needs a registered arbitration realm and arbitrator signatures and exits `1` | `escrow_cmd.go` |
| `claimable` | `get --id <0x...>`. Retired: `create`, `claim`, `cancel` | `claimable_cmd.go` |
| `p2p` | `get --id <0x...>`. Retired: `create-trade`, `settle`, `dispute`, `resolve` | `p2p_cmd.go` |
| `potso` | `heartbeat`, `user-meters`, `top`, `stake` (`lock`, `unbond`, `withdraw`, `info`), `reward` (`history`, `export`; `claim` is retired) | `potso.go`, `potso_stake.go`, `potso_rewards.go` |
| `pos` | `sweep-voids` (retired) | `pos.go` |
| `swap` | `voucher` (with `get`, `list`, `export`) | `swap.go` |
| `fees` | `status` | `fees.go` |
| `gov` | `propose`, `vote`, `finalize`, `queue`, `execute`, `show`, `list` | `gov.go` |
| `subscriptions` | `create-plan`, `update-plan`, `subscribe`, `cancel`, `get-plan`, `list-plans`, `get-subscription`, `list-by-payer`, `list-by-merchant`, `list-charges`, `config` | `subscriptions.go` |

`subscriptions create-plan` takes `--name`, `--price`, `--asset` (`NHB` or `ZNHB`,
default `NHB`), `--interval-seconds` (default `2592000`, 30 days), `--trial-seconds`
(default `0`) and `--key` (default `wallet.key`). The chain refuses a plan whose
price is below `1e18` (one whole token), whose interval is outside 86,400 to
315,360,000 seconds (one day to ten years) or whose trial is over 315,360,000
seconds (`native/subscriptions/params.go`, `registry.go`).

## Retired subcommands

Some subcommands are kept only to say they are gone: `stake claim`,
`pos sweep-voids`, `potso reward claim`, `id set-alias|set-avatar|add-address|remove-address|set-primary|rename|create-claimable|claim`,
`claimable create|claim|cancel` and `p2p create-trade|settle|dispute|resolve`.
Each prints "`<command> is retired: the node no longer serves <method> (HTTP 410)
...`" to stderr and exits `1` without contacting the node (`reportRetired` in
`cmd/nhb-cli/retired_cmd.go`). The node answers those methods with HTTP 410
because they changed the state of the one validator that handled the call,
outside block execution.

## Exit status

`main` exits with the code `run` returns, and every failure returns non-zero: a
missing argument, a usage error, an unknown command, a failed RPC call and a
retired subcommand all exit `1` (`run` in `cmd/nhb-cli/main.go`; the
`printUsage` text says "Every command that fails exits non-zero"). This includes
the single-purpose commands (`balance`, `address`, `claim-username`, `un-stake`,
`heartbeat`, the legacy `stake <amount> <key_file>` form and the `loyalty-*`
commands). Some of them print their error message to stdout rather than stderr.
