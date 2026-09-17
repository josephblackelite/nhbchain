# NHBChain Lending Overview

Welcome to the NHBChain lending protocol! This guide introduces the core
concepts that developers and end-users need to understand before interacting
with the on-chain money market.

## Core Concepts

### Supplying Liquidity
When users supply supported assets into the protocol, they receive tokenized
receipts that represent their deposit plus accrued interest. Suppliers earn
variable yields that adjust automatically based on utilization of each market.

### Borrowing Assets
Borrowers can take out over-collateralized loans against their supplied
positions. Borrowed balances accrue interest continuously until they are
repaid. To stay safe, borrowers should monitor their health factor and keep it
above the liquidation threshold.

### Collateral Management
Each asset in the market has its own Loan-to-Value (LTV) ratio and liquidation
threshold. Users can enable specific deposits as collateral. The protocol uses
oracle prices to compute the current value of every collateral asset and
compare it against open borrows.

### Liquidation Safety Net
If a borrower\'s health factor falls below 1.0, their position becomes eligible
for liquidation. Liquidators repay part of the borrower\'s debt and receive a
portion of the collateral at a discount. This mechanism keeps the markets
solvent even during periods of high volatility.

## Key Terms

- **Health Factor (HF):** A measure of how safe a borrowing position is. When
  HF > 1.0 the position is healthy; when HF ≤ 1.0 it may be liquidated.
- **Loan-to-Value (LTV):** The maximum borrowing power of a collateral asset,
  expressed as a percentage of its value.
- **Liquidation Threshold:** The collateral ratio at which a position becomes
  liquidatable. This is always greater than or equal to the LTV.
- **Utilization:** The share of supplied liquidity that is currently borrowed.
  High utilization leads to higher interest rates for borrowers and suppliers.
- **Reserve Factor:** The percentage of interest routed to the protocol
  treasury instead of depositors.

## Lifecycle of a Lending Position

1. **Supply:** A user deposits assets and enables them as collateral.
2. **Borrow:** The user borrows against their collateral up to the maximum
   allowed by the LTV.
3. **Accrual:** Interest accumulates continuously on both deposits and borrows.
4. **Health Monitoring:** The protocol recalculates health factors whenever
   prices or balances change.
5. **Repay or Adjust:** Borrowers can repay debt or add collateral to restore
   safety margins.
6. **Liquidation (if necessary):** If health falls below 1.0, liquidators can
   repay debt and seize collateral according to protocol rules.

## Next Steps

- Dive into the [On-Chain Architecture](on-chain.md) to learn how the protocol
  implements risk controls and accrues interest.
- Explore the [RPC API reference](rpc-api.md) for endpoint details.
- Follow the [Developer Guide](developer-guide.md) for a hands-on walkthrough of
  building lending experiences on NHBChain.
