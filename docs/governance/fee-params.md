# Fee policy configuration

There is no "feepolicy" governance module and no CLI path for governance fee
param queries or proposals today — `cmd/nhbctl`'s entire command set is a
single `migrate-keystore` subcommand. Fee behaviour is configured locally per
node under the `[global.fees]` TOML block (`Fees` struct in
`config/types.go`). `core/node.go`'s `buildFeePolicyFromConfig` reads that
block once at node startup and builds the runtime `fees.Policy` /
`fees.DomainPolicy` consumed by `native/fees/apply.go`. Use this page as a
reference when editing a node's `config.toml`.

## Configurable fields (`[global.fees]`)

| Field | Description |
| --- | --- |
| `FreeTierTxPerMonth` | Monthly allowance of NHB-sponsored transactions per wallet (default `100`). |
| `MDRBasisPoints` | Default merchant discount rate, expressed in basis points (default `150`), applied when an asset does not have its own override in `Assets`. |
| `OwnerWallet` | Default NHB-bech32 wallet receiving the network fee share when an asset does not override it. |
| `TransferFreeTierSpendWei` | Lifetime (or windowed) free-tier transfer spend allowance, in Wei, before the protocol-enforced transfer fee applies. |
| `TransferFreeTierWindow` | Window over which `TransferFreeTierSpendWei` resets (for example `"lifetime"`). |
| `TransferFeeCollector` | Wallet that receives the protocol-enforced transfer fee. |
| `TransferFeeBps` | Protocol-enforced fee, in basis points of the transfer amount, charged once a sender exceeds `TransferFreeTierSpendWei`. |
| `Assets` | Per-asset overrides (`Asset`, `MDRBasisPoints`, `OwnerWallet`) layered on top of the domain defaults above. |

## Changing fee configuration

Edit the `[global.fees]` block in the node's `config.toml` and restart the
node. `buildFeePolicyFromConfig` only runs at startup, so there is no runtime
or governance-driven way to change these values without a config edit and
restart today.

For additional context on how each parameter affects runtime behaviour see the
[fee policy](../fees/policy.md) and [fee routing](../fees/routing.md) guides.
