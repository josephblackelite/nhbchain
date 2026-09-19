# Fee policy configuration

Fee behavior is configured per node in the `[global.Fees]` TOML block (the
`Fees` struct in `config/types.go`). `Node.SetGlobalConfig` (`core/node.go`)
builds the runtime `fees.Policy` and `TransferGasPolicy` from that block; it is
called from `cmd/nhb/main.go` and `cmd/consensusd/main.go` at startup.

There is no governance path for these values. No proposal kind writes to
`[global.Fees]`, `cmd/nhbctl` has a single `migrate-keystore` subcommand, and
the only fee-related governance keys are `fees.baseFee` and
`protocol.feeRateBps`, neither of which is read by any chain logic (see
[params](./params.md)). To change a fee value, edit `config.toml` and restart
the node. Every validator has to run the same values, because transaction
execution depends on them.

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
enabled`). `cmd/consensusd` calls `ValidateConfig` at startup
(`cmd/consensusd/main.go:139`); `cmd/nhb` does not.

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
