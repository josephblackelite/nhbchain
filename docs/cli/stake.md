# `nhb-cli stake`

The `stake` subcommand of `nhb-cli` wraps the privileged staking RPC helpers.
Every request requires the `NHB_RPC_TOKEN` environment variable to be set so the
CLI can attach the `Authorization` header. Use the global `--rpc` flag if you
need to target a remote node.

## Claim accrued rewards

```bash
nhb-cli stake claim nhb1exampledelegator...
```

Running `stake claim` calls the `stake_claimRewards` RPC and prints a single
summary line when the request succeeds:

```
Minted 7425000000000000000000 ZNHB for 3 period(s). Next claim after 2024-08-02T12:00:00Z.
```

The CLI then dumps the refreshed account snapshot so you can confirm the
ZapNHB balance increase alongside any other staking metadata.

If the payout window has not elapsed yet, the node responds with HTTP 409 and
an error payload that includes the next eligible timestamp. The CLI turns that
into a friendly message:

```
Not yet eligible. Next at 2024-08-09T12:00:00Z (1723204800).
```

Use `nhb-cli stake preview` to double-check the payout schedule before you
attempt to claim if you want to avoid the 409 response.

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
