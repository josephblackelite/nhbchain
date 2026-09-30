# Staking CLI

The `nhb-cli` binary provides a handful of staking helpers in addition to the
legacy `stake` and `un-stake` flows. All staking RPC calls require an
`Authorization` header, so make sure the `NHB_RPC_TOKEN` environment variable is
set before using any of these commands.

You can override the RPC endpoint with the global `--rpc` flag if you need to
point at a remote node.

## View the current position

```bash
nhb-cli stake position nhb1exampleaddress
```

`stake position` prints the share count, the last staking index that was
applied, and the timestamp of the most recent payout for the supplied address.
This is the quickest way to verify that rewards are accruing on schedule.

## Preview the next rewards claim

```bash
nhb-cli stake preview nhb1exampleaddress
```

`stake preview` calls `stake_previewClaim` on the node and returns the amount of
ZapNHB that would be minted by a claim right now as well as the timestamp when
the next payout window opens. Use this before sending a claim to double check
that the window has actually elapsed.

## Claim rewards (retired)

```bash
nhb-cli stake claim nhb1exampleaddress
```

`stake claim` is retired: the node no longer serves the `stake_claimRewards` RPC
(HTTP 410), so the command says so and exits non-zero without contacting the
node. Claiming staking rewards is a signed `TxTypeStakeClaimRewards`
transaction sent through `nhb_sendTransaction`; this CLI does not build it yet.

## Legacy staking shortcut

The original shortcut remains available:

```bash
nhb-cli stake 1000000000000000000 wallet.key
```

This path signs and sends a `TxTypeStake` transaction that stakes the given
amount of ZapNHB from the key's own account. It is left in place for
compatibility, but new scripts should prefer the explicit subcommands above.
