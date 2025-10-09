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

## Claim rewards (and optionally restake them)

```bash
nhb-cli stake claim wallet.key
# or automatically delegate anything that was minted
nhb-cli stake claim --compound wallet.key
```

`stake claim` loads the signing key, calls `stake_claimRewards`, and prints a
snapshot of the updated account metadata including balances, validator
selection, and any pending unbonds. When the `--compound` flag is present the
CLI automatically submits a `stake_delegate` transaction for the minted amount
(so long as it is non-zero). The existing delegation is reused when one is set.

## Legacy staking shortcut

The original shortcut remains available:

```bash
nhb-cli stake 1000000000000000000 wallet.key
```

This path still sends a `stake_delegate` transaction with the provided amount
of ZapNHB. It is left in place for compatibility, but new scripts should prefer
the explicit subcommands above.
