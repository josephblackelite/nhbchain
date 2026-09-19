# `nhb-cli stake`

`nhb-cli stake` has three read/claim subcommands (`position`, `preview`,
`claim`) that call the staking RPC methods, plus a legacy form
`stake <amount> <key_file>` that signs and sends a stake transaction. The code is
in `cmd/nhb-cli/stake.go` (subcommands) and `cmd/nhb-cli/main.go` (the legacy
`stake` function).

Every RPC call the subcommands make is sent with the bearer token from the
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

## Claim rewards: `stake claim` does not work

```bash
nhb-cli stake claim <address>
```

This subcommand sends the `stake_claimRewards` RPC, and that RPC is disabled on
the node. `handleStakeClaimRewards` answers every call with HTTP 410 and error
code `-32060`, so the CLI prints

```
RPC error -32060: this method is disabled; sign a transaction (TxTypeStake/TxTypeUnstake/TxTypeStakeClaim/TxTypeStakeClaimRewards) via nhb_sendTransaction instead, so the caller's own signature authorizes the action
```

and exits with status `1`. The method was disabled because it changed state
outside block execution and trusted a caller-supplied address
(comment above `stakeRPCDisabledMessage` in `rpc/stake_handlers.go`).

Rewards are claimed with a signed transaction of type `TxTypeStakeClaimRewards`
(`0x34`, `core/types/transaction.go`). It carries no payload, is signed by the
account that is claiming, and is submitted with `nhb_sendTransaction`
(`applyStakeClaimRewards` in `core/state_transition.go`). `nhb-cli` has no
command that builds this transaction. Claiming before a payout period has
elapsed fails in the state transition with the staking "not due" error.

The CLI's handling of "Not yet eligible" (HTTP 409) and "staking not ready"
responses in `runStakeClaim` is left over from the earlier RPC; the node no
longer returns those responses for this method.

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

Errors (bad key file, RPC error) are printed to stdout and the process still
exits with status `0`, so scripts cannot rely on the exit code for this form.

To stake and register as a validator candidate in one transaction, use
`register-validator` (see [staking.md](./staking.md)).
