# `nhb-cli stake`

`nhb-cli stake` has two read subcommands (`position`, `preview`) that call the
staking RPC methods, a `claim` subcommand that is retired, and a legacy form
`stake <amount> <key_file>` that signs and sends a stake transaction. The code is
in `cmd/nhb-cli/stake.go` (subcommands) and `cmd/nhb-cli/main.go` (the legacy
`stake` function).

Every RPC call `position` and `preview` make is sent with the bearer token from the
`NHB_RPC_TOKEN` environment variable; the CLI stops with "privileged RPC call
requires NHB_RPC_TOKEN to be set" if it is empty. Use the global
`--rpc <url>` flag, or the `RPC_URL` environment variable, to target a node
other than `http://localhost:8080`.

The RPC handlers are in `rpc/stake_handlers.go`. While the node's `staking`
module is paused they answer `staking module paused` (HTTP 503), and they are
subject to the node's per-source rate limit.

## Inspect the current position

```bash
nhb-cli stake position <address>
```

Calls `stake_getPosition` and prints:

```
Stake position for <address>
  Shares:       <account.StakeShares>
  Last index:   <account.StakeLastIndex>
  Last payout:  <RFC3339 UTC> (<unix seconds>)      # or "never" when 0
```

The values come straight from the account's `StakeShares`, `StakeLastIndex` and
`StakeLastPayoutTs` fields. `<address>` must be a bech32 address.

## Preview the next claim

```bash
nhb-cli stake preview <address>
```

Calls `stake_previewClaim` and prints:

```
Stake rewards preview for <address>
  Claimable now: <payable> ZapNHB
  Next payout:   <RFC3339 UTC> (<unix seconds>)     # or "unavailable" when 0
```

`payable` and `nextPayoutTs` are computed by the node's `StakePreviewClaim` for
the current time. The public `nhb_getBalance` RPC (no token needed) also returns
the same preview as `pendingStakingRewards`.

## Claim rewards: `stake claim` is retired

```bash
nhb-cli stake claim <address>
```

This subcommand no longer contacts the node. It prints

```
Error: nhb-cli stake claim is retired: the node no longer serves stake_claimRewards (HTTP 410), because it changed validator-local state outside the block pipeline, and this CLI has no replacement for it yet.
```

to stderr and exits with status `1` (`reportRetired` in
`cmd/nhb-cli/retired_cmd.go`). The node itself answers `stake_claimRewards`
with HTTP 410 and error code `-32060` (`handleStakeClaimRewards` and
`stakeRPCDisabledMessage` in `rpc/stake_handlers.go`): the method changed state
outside block execution and trusted a caller-supplied address.

Rewards are claimed with a signed transaction of type `TxTypeStakeClaimRewards`
(`0x34`, `core/types/transaction.go`). It carries no payload, is signed by the
account that is claiming, and is submitted with `nhb_sendTransaction`
(`applyStakeClaimRewards` in `core/state_transition.go`). `nhb-cli` has no
command that builds this transaction. Claiming before a payout period has
elapsed fails in the state transition with the staking "not due" error.

## Legacy shortcut: `stake <amount> <key_file>`

```bash
nhb-cli stake 1000000000000000000 wallet.key
```

Builds a `TxTypeStake` (`0x06`) transaction with `Value` set to the amount (a
positive base-10 integer, in ZNHB base units), gas limit `21000`, gas price `1`,
and no payload; signs it with the key file; and submits it with
`nhb_sendTransaction`. With no payload the node stakes to the signer's own
address (`StakeDelegate` treats an empty validator as the delegator itself). On
success the CLI prints:

```
Successfully sent stake transaction for <amount> ZapNHB.
Check the node logs for confirmation and wait for the next block.
```

Errors (bad key file, RPC error) are printed to stdout and the process exits with
status `1`. A command line that is not `<positive integer> <key_file>` prints the
`stake` usage text to stderr and exits `1`.

To stake and register as a validator candidate in one transaction, use
`register-validator` (see [staking.md](./staking.md)).
