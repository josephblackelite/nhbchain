# On-Chain Lending Architecture

This document provides a technical overview of how the NHBChain lending module
operates on-chain. It is intended for smart contract developers, auditors, and
infrastructure partners who need to understand the protocol\'s accounting and
risk controls.

## Interest Rate Model

Each market tracks supply utilization `U = totalBorrowed / totalSupplied`. The
protocol applies a piecewise-linear interest rate curve:

- **Base Rate:** Applied when utilization is zero, representing the minimum
  borrow APR.
- **Slope 1:** Gradually increases the borrow rate from the base rate until the
  optimal utilization point.
- **Slope 2:** A steeper increase that kicks in after optimal utilization to
  discourage further borrowing and incentivize more supply.

Borrow interest is compounded each block by updating the borrow index. The
supply rate is derived from the borrow rate using the reserve factor `r` and the
protocol fee share `p`:

```
supplyRate = borrowRate * U * (1 - r - p)
```

All rates are quoted per-second but can be accumulated to APRs for user-facing
interfaces.

## Interest Accrual Mechanics

1. **Accrual Trigger:** Every market accrues interest during state-changing
   operations (supply, withdraw, borrow, repay, liquidation, and collateral
   withdrawals).
2. **Borrow Index Update:** The protocol calculates the time delta since the
   last accrual and multiplies it by the current borrow rate to update the
   borrow index.
3. **Reserve Growth:** A portion of the interest (based on the reserve factor)
   and the configured protocol fee share is redirected to the protocol fee
   accrual.
4. **Supplier Yield:** The remaining interest is distributed proportionally to
   suppliers by increasing the exchange rate between deposit receipts and the
   underlying asset.

The protocol tracks protocol and developer fees in a `FeeAccrual` structure.
Those balances can be withdrawn to external accounts, reducing the pool's
reported liquidity while keeping historical accounting intact.

## Collateral Evaluation

- **Oracle Prices:** Prices are fetched from NHBChain\'s decentralized oracle
  network and normalized to 18 decimals.
- **Collateral Factor:** Each asset has an LTV (maximum borrowing power) and a
  liquidation threshold (safety buffer).
- **Borrow Power:** The protocol sums the USD value of enabled collateral assets
  multiplied by their LTV to compute total borrowing capacity.
- **Shortfall:** If the USD value of borrows exceeds the liquidation-adjusted
  collateral value, the account is flagged for liquidation.

## Liquidation Flow

1. **Detection:** When an account\'s health factor drops below 1.0, the
   position becomes liquidatable.
2. **Repayment:** A liquidator specifies the asset and repayment amount to cover
   part of the borrower\'s debt.
3. **Seizure:** The contract transfers collateral to the liquidator, applying an incentive bonus defined per market.
4. **Close Factor:** A maximum percentage of the outstanding borrow can be
   liquidated in a single transaction to prevent full wipeouts in thin markets.

## Risk Parameters

All risk parameters (LTV, liquidation threshold, reserve factor, close factor,
liquidation bonus) are governed on-chain. Governance proposals update the
configuration contract, which is referenced by every market instance. Changes
become active immediately after the proposal execution block.

## Events and Indexing

Markets emit events for all critical actions, including accrual updates,
liquidations, and reserve transfers. Indexing services can subscribe to these
logs to provide real-time analytics and alerting for borrowers.

For implementation details, refer to the NHBChain lending smart contracts in the
`native` repository and the associated unit tests in `tests/`.
