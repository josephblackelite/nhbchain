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
  is empty. Balance lookups (`nhb_getBalance`) are sent without a token.
- **Key files.** A `<key_file>` argument is either a raw private-key file, as
  written by `generate-key`, or a JSON V3 keystore whose passphrase is in
  `NHB_KEYSTORE_PASSPHRASE` (`loadPrivateKey` in `cmd/nhb-cli/main.go`). A file
  that contains the old placeholder key material is refused.

## Top-level commands

Key and account:

- `generate-key` -- writes a new key to `wallet.key` in the current directory
  (mode `0600`) and prints its address. It writes with `os.WriteFile`, so an
  existing `wallet.key` in that directory is overwritten.
- `address <key_file>` -- prints the address for a key file.
- `balance <address>` -- prints username, NHB and ZapNHB balances, stake, locked
  stake, delegation, validator registration, pending unbonds and nonce.
- `claim-username <username> <key_file>` -- sends `TxTypeRegisterIdentity`
  (`0x02`) with the username as data (gas limit `50000`).
- `keystore import --out <path>` -- see [keystore](./keystore.md).

Transfers:

- `send-nhb`, `send-znhb` -- see [send](./send.md). (`send-nhb` is not in the
  binary's own usage text, but it is dispatched.)

Staking and validators:

- `stake ...`, `un-stake <amount> <key_file>`, `register-validator <amount>
  <key_file>`, `deregister-validator <key_file>`, `set-reward-beneficiary
  <address|""> <key_file>` -- see [staking](./staking.md) and [stake](./stake.md).
- `heartbeat <key_file>` -- sends `TxTypeHeartbeat` (`0x08`) with zero value.

Contracts:

- `deploy <bytecode_file> <key_file>` -- decodes the file's contents with
  `hex.DecodeString` (a bare hex string: no `0x` prefix, no trailing newline) and
  sends a `TxTypeTransfer` transaction with no `to` address and the bytecode as
  data (gas limit `1000000`, gas price `1`).

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
| `id` | `set-alias`, `set-avatar`, `add-address`, `remove-address`, `set-primary`, `rename`, `resolve`, `reverse`, `create-claimable`, `claim` | `identity_cmd.go` |
| `escrow` | `create`, `get`, `fund`, `release`, `refund`, `expire`, `dispute`, `resolve`, `create-realm` | `escrow_cmd.go` |
| `claimable` | `create`, `claim`, `cancel`, `get` | `claimable_cmd.go` |
| `p2p` | `create-trade`, `get`, `settle`, `dispute`, `resolve` | `p2p_cmd.go` |
| `potso` | `heartbeat`, `user-meters`, `top`, `stake`, `reward` | `potso.go` |
| `pos` | `sweep-voids` | `pos.go` |
| `swap` | `voucher` (with `get`, `list`, `export`) | `swap.go` |
| `fees` | `status` | `fees.go` |
| `gov` | `propose`, `vote`, `finalize`, `queue`, `execute`, `show`, `list` | `gov.go` |
| `subscriptions` | `create-plan`, `update-plan`, `subscribe`, `cancel`, `get-plan`, `list-plans`, `get-subscription`, `list-by-payer`, `list-by-merchant`, `list-charges`, `config` | `subscriptions.go` |

## Exit status

Commands implemented as `run...Command` functions (`send-*`, `stake`, the command
groups, `keystore`) return a non-zero status on failure. `register-validator`,
`deregister-validator` and `set-reward-beneficiary` also exit `1` on failure. The
older single-purpose commands (`balance`, `address`, `claim-username`, `un-stake`,
`heartbeat`, `deploy`, the legacy `stake <amount> <key_file>`, and the
`loyalty-*` commands)
print their error and exit `0`, so scripts should not rely on their exit code.
