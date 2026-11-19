# Fee policy configuration

Fee behavior is configured per node in the `[global.Fees]` TOML block (the
`Fees` struct in `config/types.go`). `Node.SetGlobalConfig` (`core/node.go`)
builds the runtime `fees.Policy` and `TransferGasPolicy` from that block; it is
called from `cmd/nhb/main.go` and `cmd/consensusd/main.go` at startup.

Until the chain rule `TransferFeeGovernanceActivationHeight` is reached (it is `0`,
off, in every shipped file) there is no governance path for the transfer fee. No
proposal kind writes to `[global.Fees]`, `cmd/nhbctl` has a single
`migrate-keystore` subcommand, and the only fee-related `param.update` keys are
`fees.baseFee` and `protocol.feeRateBps`, neither of which is read by any chain
logic (see [params](./params.md)). To change a fee value, edit `config.toml` and
restart the node. Every validator has to run the same values, because
transaction execution depends on them. From the height, the transfer fee schedule
is governed on chain: see [Governing the transfer fee schedule](#governing-the-transfer-fee-schedule-policyfeeparams)
below. The domain rates (`MDRBasisPoints`) stay in the file under both rules.

## Fields (`[global.Fees]`)

Defaults are applied by `defaultGlobalConfig` in `config/config.go` when a key
is absent from the file.

| Field | Default | Description |
| --- | --- | --- |
| `FreeTierTxPerMonth` | `100` | Free-tier transactions per payer per UTC month for the fee domains (see [fee policy](../fees/policy.md)). |
| `MDRBasisPoints` | `150` | Fee rate, in basis points of the transferred amount, for the fee domains once the free tier is used up. Per-asset values in `Assets` override it. |
| `OwnerWallet` | set in code | Default bech32 wallet that receives domain fees when an asset entry has no wallet of its own. |
| `TransferFreeTierSpendWei` | `1000000000000000000000` (1,000 tokens) | Per-wallet, per-asset spend allowance before the protocol transfer fee applies. If this is `0` or empty, `buildTransferGasPolicyFromConfig` sets the policy `Enabled = false`. That removes the free tier and, on the NHB path, the credit to the collector. It does not stop the fee being computed and charged; see [fee policy](../fees/policy.md#1-protocol-transfer-fee-transfergaspolicy). To turn the fee off, set `TransferFeeBps` and `TransferFeeBpsZNHB` to `0`. |
| `TransferFreeTierWindow` | `lifetime` | `lifetime` or `monthly`. Any other value fails `ValidateConfig`. |
| `TransferFeeCollector` | empty | Bech32 wallet that receives the transfer fee. When empty the node's escrow fee treasury address is used. |
| `TransferFeeBps` | `20` | Transfer fee for NHB, in basis points of the transfer amount. |
| `TransferFeeBpsZNHB` | `10` | Transfer fee for ZNHB, in basis points of the transfer amount. |
| `Assets` | NHB and ZNHB entries | List of `[[global.Fees.Assets]]` tables with `Asset`, `MDRBasisPoints` and `OwnerWallet`. |

`ValidateConfig` (`config/validate.go`) additionally requires
`TransferFreeTierSpendWei >= 0`, and requires an `OwnerWallet` for `ZNHB` in
`Assets` whenever a `ZNHB` asset entry exists
(`fees: route_wallet_by_asset.ZNHB must be configured when ZNHB fees are
enabled`). `cmd/consensusd` calls `ValidateConfig` at startup and
refuses to start on the first problem (`cmd/consensusd/main.go:141`). `cmd/nhb`
runs the same checks (`config.ConfigProblems`) at startup and only logs each
problem as a `configuration problem` warning; it never refuses to start
(`cmd/nhb/config_check.go`).

Example (the `Fees` struct fields in `config/types.go` are PascalCase and carry no `toml` tags; the decoder, `github.com/BurntSushi/toml` v1.5.0, tries an exact key match first and then falls back to a case-insensitive one):

```toml
[global.Fees]
  FreeTierTxPerMonth = 100
  MDRBasisPoints = 150
  OwnerWallet = "<bech32 NHB address>"
  TransferFreeTierSpendWei = "1000000000000000000000"
  TransferFreeTierWindow = "lifetime"
  TransferFeeBps = 20
  TransferFeeBpsZNHB = 10

  [[global.Fees.Assets]]
    Asset = "NHB"
    MDRBasisPoints = 150
    OwnerWallet = "<bech32 NHB address>"

  [[global.Fees.Assets]]
    Asset = "ZNHB"
    MDRBasisPoints = 200
    OwnerWallet = "<bech32 ZNHB address>"
```

See [fee policy](../fees/policy.md) for how each field is applied and
[fee routing](../fees/routing.md) for where fees are credited.

## Governing the transfer fee schedule (`policy.feeParams`)

Two chain rules, both off (`0`) in every shipped file, carry the one-policy-for-every-transfer
design. Both are chain rules: the same value on every validator, set above the chain tip, all
validators restarted together (see [consensus invariants](../consensus/invariants.md)).

**One fee for every transfer: `UniformTransferFeeActivationHeight`.** Before it, a transfer that
names a fee domain (`pos`, `p2p`, `otc`) pays the protocol transfer fee and, on top of it, the
domain's merchant rate. From the height it pays exactly what an ordinary transfer pays: the same
free allowance per wallet and asset, the same NHB and ZNHB rate. The fee is credited to the
domain's route wallet when one is configured for the asset and to the transfer fee collector
otherwise. The domain's fee totals and the `fees.applied` event are still written; the domain's
monthly counters stop advancing. A domain configured with a fee payer of `recipient` is not
honoured: the sender pays, as for any transfer. The merchant rates and the per-domain free tier
are not used from the height and are not governed.

**A governed schedule: `TransferFeeGovernanceActivationHeight` and `TransferFeeGovernanceDelayBlocks`.**
From the height a proposal of the kind `policy.feeParams` is accepted (below it the kind is
refused like an unknown one). Its payload:

```json
{"nhbFeeBps": 20, "znhbFeeBps": 10, "freeAllowanceWei": "1000000000000000000000", "memo": "optional"}
```

| Field | Rule |
| --- | --- |
| `nhbFeeBps`, `znhbFeeBps` | Required whole numbers, `0` to `100` (one percent). Not a string, a fraction or `null`. |
| `freeAllowanceWei` | Required decimal string of digits only (no sign, exponent or leading zero), at most 10^27 (a billion tokens). |
| `allowZeroAllowance` | `true` only together with `"freeAllowanceWei": "0"`, which means every transfer pays the fee. A zero allowance without the flag, or the flag with a positive allowance, is refused. |
| `memo` | Optional, at most 256 bytes. |

An unknown field, a field repeated, a key in another case and trailing text are refused. The
payload is checked when the proposal is submitted and again when it executes.

A passed proposal, once its timelock has elapsed and it is executed, is stored in chain state
(parameter key `fees.transferSchedule`, not writable by `param.update`) as a *pending* schedule with
the block height it takes effect from: the block that executed it plus the delay. The delay is
`TransferFeeGovernanceDelayBlocks`, raised to 100 blocks when lower; a value below 100 is reported at
start-up (`config.FeeScheduleProblems`) and refuses the start under `[governance]
EnforceWindowFloors`. Because the height is part of the stored record, every validator and every
follower applies the new schedule in the same block. From that block the schedule's rates and
allowance replace `TransferFeeBps`, `TransferFeeBpsZNHB` and `TransferFreeTierSpendWei` of the file; the
collector, the allowance window (`TransferFreeTierWindow`) and the monthly free tier are not part
of it. A proposal that executes while another is still waiting for its block replaces it (the audit
record of the execution names the replaced proposal); one that executes after the earlier schedule
took effect leaves that schedule in force until its own height. A stored record that is not a
well-formed schedule within the bounds is ignored, on every validator alike.

`fees_getTransferSchedule` (see [fees query API](../api/fees-query.md)) shows the schedule the next
block will charge and any change that is waiting, with the blocks left.

## The strict free allowance (`TransferFeeStrictAllowanceActivationHeight`)

Until this chain rule's height (0, off, in every shipped file) a transfer is free in full while the sender's spend so far
in the asset is below the free allowance, so the transfer that crosses the line is free in full: a first transfer of
1,000,000 NHB against an allowance of 1,000 pays no fee. From the height the allowance is strict. The free part of a
transfer is what is left of the allowance (never below zero, never more than the amount) and the fee is the asset's rate
on the rest:

```
free       = min(max(0, limit - spent), amount)
fee        = (amount - free) * bps / 10,000      (integers, rounded down)
```

A first transfer of 1,100 NHB against 1,000 pays 0.20% of 100 (0.2 NHB); a first transfer of 1,000,000 NHB pays 0.20% of
999,000. The limit is the configured allowance or, once a governed schedule is in force, its allowance. The rule applies to
the ordinary fee of both assets and therefore to a transfer that names a fee domain under the uniform rule. A transfer
carried by a paymaster is unchanged (the sponsor pays the fee on the whole amount, the sender's allowance is not
consulted, and the transfer still counts as the sender's spend). Where the optional monthly free tier
(`TransferMonthlyFreeTierActivationHeight`) applies it still waives the whole remaining fee. `fees_getTransferQuote`
returns `freeWei`, `chargeableWei` and `strictAllowance` besides `eligible`, `feeWei` and `feeBps` (under the strict rule
`eligible` is true only when the allowance covers the whole transfer); `fees_getTransferStatus` returns
`strictAllowance`; `fees_getTransferSchedule` returns `strictAllowance` with `active` and `activationHeight`.
