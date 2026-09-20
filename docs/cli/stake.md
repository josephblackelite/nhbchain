# `nhb-cli stake`

The `stake` subcommand of `nhb-cli` wraps the privileged staking RPC helpers.
Every request requires the `NHB_RPC_TOKEN` environment variable to be set so the
CLI can attach the `Authorization` header. Use the global `--rpc` flag if you
need to target a remote node.

## Claim accrued rewards (retired)

```bash
nhb-cli stake claim nhb1exampledelegator...
```

`stake claim` is retired. The node no longer serves the `stake_claimRewards`
RPC (it answers HTTP 410): the method minted rewards on the state of the one
validator that handled the call, outside block execution. The command says so
and exits non-zero without contacting the node. Claiming staking rewards is a
signed `TxTypeStakeClaimRewards` transaction sent through `nhb_sendTransaction`;
this CLI does not build it yet.

Use `nhb-cli stake preview` to see what a claim would pay and when the next
payout window opens.

## Inspect the current position

```bash
nhb-cli stake position nhb1exampledelegator...
```

This call returns the share count, the last applied index, and the timestamp of
the most recent payout for the supplied address. It is useful for confirming
that the staking engine is tracking your delegations correctly.

## Preview the next claim window

```bash
nhb-cli stake preview nhb1exampledelegator...
```

The preview helper estimates the amount of ZapNHB that would be minted by a
claim issued right now and reports when the next claim window opens. This is
handy for scripting because it avoids sending premature `stake_claimRewards`
requests.
