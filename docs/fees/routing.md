# Fee routing

Where the two fee mechanisms described in [fee policy](./policy.md) credit
their proceeds. Everything here is on-chain account crediting done by
`core/state_transition.go`; there is no separate fee-router module.

## Domain fees

`applyTransactionFee` debits the fee from the payer (the sender, since the
configuration builder never sets `FeePayer` to `recipient`) and credits it as
follows.

| Asset | Recipient |
| --- | --- |
| `owner_fee_wallet_nhb` | NHB-denominated wallet that accrues operator fees from retail transactions and POS MDR collections. |
| `znhb_proceeds_wallet` | Receives the ZNHB (stablecoin) portion of collected fees and routes it to the treasury stabilisation program. |

- The asset's route wallet comes from `[[global.Fees.Assets]]`; an asset entry
  with no `OwnerWallet` uses `[global.Fees] OwnerWallet`
  (`buildFeePolicyFromConfig`, `core/node.go`). If the resolved wallet is the
  zero address and a fee is due, the transaction fails with `fees: missing
  route wallet for asset <ASSET>`.
- **Buyback share.** For NHB fees, when the node has a genesis buyback
  configuration (`hasBuybackConfig`), `feeShareBps` of the fee (default `2000`,
  20%, or the value set by a `policy.buybackParams` proposal) is credited to the
  buyback accrual account instead of the `OwnerWallet`, and the accrual ledger
  is updated. With no buyback configuration the whole fee goes to the
  `OwnerWallet`. See [tokenomics](../tokenomics/tokenomics.md#6-the-treasury-buyback-engine).
- ZNHB domain fees are credited to the route wallet as ordinary balance; they
  are not adjusted against the Sale Pool or Reward Pool ledgers in
  `applyTransactionFee`.

The `ValidateConfig` rule in `config/validate.go` requires an `OwnerWallet` for
`ZNHB` in `[[global.Fees.Assets]]` whenever a ZNHB asset entry exists
(`cmd/consensusd` runs it at startup).

## Transfer fees

The protocol transfer fee is credited to `TransferFeeCollector`, or to the
node's escrow fee treasury when unset. There is no buyback share on this fee.
When the collector is the chain's admin wallet, ZNHB transfer fees are also
credited to the ZNHB Reward Pool ledger. Details are in
[fee policy](./policy.md#1-protocol-transfer-fee-transfergaspolicy).

## Configuration

See [fee configuration](../governance/fee-params.md). Key names are the
PascalCase Go field names (`OwnerWallet`, `Assets`, `Asset`, `MDRBasisPoints`);
the TOML decoder prefers an exact match but also accepts a different case.

## Observing routing

## Routing flow

The runtime enforces deterministic routing whenever a transaction includes a fee
component. The high-level flow is illustrated below.

```mermaid
description Top-level routing flow for POS payments
sequenceDiagram
  participant POS as POS Paymaster
  participant Router as Fee Router Module
  participant NHB as owner_fee_wallet_nhb
  participant ZNHB as znhb_proceeds_wallet
  POS->>Router: Submit settlement (gross amount, fee quote)
  Router->>Router: Evaluate domain (POS/P2P/OTC), exemptions
  Router->>NHB: Transfer NHB fee share
  alt Stablecoin component present
    Router->>ZNHB: Transfer ZNHB fee share
  end
  Router-->>POS: Confirm net settlement amount
```

### Domain-specific logic

* **POS transactions:** Apply MDR split first, then deliver the net settlement to
the merchant. NHB denominated fees route to `owner_fee_wallet_nhb`; any stablecoin portion uses `znhb_proceeds_wallet`.
* **P2P transfers:** Charge against the sender's balance. Free-tier coverage sets
the fee to zero; otherwise the NHB share is deposited into `owner_fee_wallet_nhb`.
* **OTC deals:** Support mixed legs. NHB flows target `owner_fee_wallet_nhb`.

### Implementation hooks

```mermaid
description Fee router hook ordering
flowchart TD
  A[Tx execution] --> B[Calculate fee obligation]
  B --> C{Exemption?}
  C -- Yes --> D[Bypass routing]
  C -- No --> E[Split by currency]
  E --> F[route_nhb(owner_fee_wallet_nhb)]
  E --> G[route_znhb(znhb_proceeds_wallet)]
  F --> I[Record telemetry]
  G --> I
  I --> J[Emit events for analytics stream]
```

The node emits `FeeRouted` events tagged with the domain, payer, and destination
wallet. Observability pipelines consume the events to power revenue dashboards
and reconciliation reports.

## Governance controls

Routing targets and split ratios are governed parameters. Refer to
[fee parameters](../governance/fee-params.md) for the full list and proposal
workflow. When updating wallet addresses always include a dry-run query of the
new configuration and confirm the receiving account has been initialised.
