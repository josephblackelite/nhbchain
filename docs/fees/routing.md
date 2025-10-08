# Fee routing

Collected fees are split across the ecosystem to fund operations, reserves, and
stability programs. This document describes the current routing logic and the
on-chain accounts that receive the proceeds.

## Destination wallets

| Target | Description |
| --- | --- |
| `owner_fee_wallet_nhb` | NHB-denominated wallet that accrues operator fees from retail transactions and POS MDR collections. |
| `znhb_proceeds_wallet` | Receives the ZNHB (stablecoin) portion of collected fees and routes it to the treasury stabilisation program. |
| `owner_fee_wallet_usdc` | Off-chain custody wallet that buffers USDC-denominated inflows before sweeping to banking rails. |

Each wallet address is stored in the `FeePolicy` module parameters. Governance
proposals can rotate the addresses or adjust the split ratios when treasury
requirements change. Domains now expose an asset map that binds every accepted
currency (NHB, ZNHB, and future additions) to a specific MDR basis-point value
and routing wallet, making fee distribution explicit on a per-asset basis.

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
* **OTC deals:** Support mixed legs. NHB flows target `owner_fee_wallet_nhb`
while off-chain settlement instructions are signed for `owner_fee_wallet_usdc`.

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
  E --> H[route_offchain(owner_fee_wallet_usdc)]
  F --> I[Record telemetry]
  G --> I
  H --> I
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
