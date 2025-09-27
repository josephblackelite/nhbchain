# Lending Developer Guide

This guide walks through building a third-party lending experience on NHBChain.
It covers end-to-end integration patterns, monetization options, and UI safety
checks.

## How to Build a Lending App on NHBChain

### 1. Discover Markets
- Call [`lend_listMarkets`](rpc-api.md#lend_listmarkets) to fetch available
  markets and utilization metrics.
- Cache the results and refresh every few seconds to update rate displays.

### 2. Show Market Health
- Use [`lend_getMarket`](rpc-api.md#lend_getmarket) to display supply and borrow
  APRs, reserve factors, and risk parameters.
- Highlight utilization and remaining liquidity so users understand how their
  actions affect the pool.

### 3. Authenticate the User
- Prompt the user to connect their NHBChain wallet.
- Use [`lend_getUserAccount`](rpc-api.md#lend_getuseraccount) to display current
  collateral, borrows, and the Health Factor (HF).

### 4. Supply Assets
- Let users choose supported assets and specify amounts.
- Submit [`lend_supply`](rpc-api.md#lend_supply) transactions with clear signing
  prompts.
- Update the UI optimistically while polling for confirmation.

### 5. Enable Collateral
- Call [`lend_enableCollateral`](rpc-api.md#lend_enablecollateral) so the
  deposit can back future loans.

### 6. Borrow Responsibly
- Before calling [`lend_borrow`](rpc-api.md#lend_borrow), calculate the
  projected health factor using the formulas below.
- Provide warnings for risky transactions and enforce protocol-defined limits.

### 7. Repay or Adjust
- Offer [`lend_repay`](rpc-api.md#lend_repay) and
  [`lend_repayWithCollateral`](rpc-api.md#lend_repaywithcollateral) actions so
  users can manage debt even during volatility.

### 8. Monitor Health
- Subscribe to account updates or refresh `lend_getUserAccount` periodically to
  notify borrowers when their HF approaches 1.0.

## Monetizing Your App with Borrower Fees

Third-party apps can earn revenue by routing a portion of each NHB borrow to
their own address using [`lend_borrowNHBWithFee`](rpc-api.md#lend_borrownhbwithfee).

```json
{
  "address": "nhb1qborrower...",
  "amount": "500.0",
  "feeRecipient": "nhb1qmyapp...",
  "feeBps": 100
}
```

- `feeRecipient` should be your application\'s treasury wallet.
- `feeBps` is specified in basis points (1/100 of a percent). A value of 100
  equals a 1% fee.
- The protocol transfers `amount * feeBps / 10_000` NHB to the fee recipient as
  soon as the borrow is executed.
- Always display the fee and resulting net borrow amount to maintain user trust.

## UI Formulas & Best Practices

Use the following formulas to keep interface calculations aligned with the
protocol. All USD values should be derived from the latest oracle prices.

### Borrow Power

```
borrowPowerUSD = Σ(collateralValueUSD_i * LTV_i)
```

### Current Health Factor

```
healthFactor = Σ(collateralValueUSD_i * LiquidationThreshold_i) / totalBorrowedUSD
```

If `totalBorrowedUSD` is zero, display HF as `∞`.

### Max Borrowable Amount

```
maxBorrowableUSD = max(0, borrowPowerUSD - totalBorrowedUSD)
```

Limit the user\'s requested borrow amount to this value.

### Projected Health Factor

When simulating a new borrow of `newBorrowUSD`:

```
projectedBorrowedUSD = totalBorrowedUSD + newBorrowUSD
projectedHF = Σ(collateralValueUSD_i * LiquidationThreshold_i) / projectedBorrowedUSD
```

Show warnings when `projectedHF < 1.2` and block the transaction when it falls
below 1.0.

### Liquidation Price Estimate

For volatile collateral, offer an indicative price at which the position would
reach HF = 1.0:

```
criticalPrice = (totalBorrowedUSD / (collateralAmount * LiquidationThreshold))
```

### Other Best Practices

- Display accrued interest separately from principal so users understand their
  debt growth.
- Provide explicit confirmation modals that include utilization, borrow APR, and
  projected HF.
- Encourage borrowers to keep at least a 20% buffer between the liquidation
  threshold and their projected HF.

## Working Offline with JSON Caches

For rapid prototyping and design, use the static JSON files in
[`cache/`](cache) to emulate live RPC responses:

- `lend_getMarket.json`: Mirrors a typical `lend_getMarket` result.
- `lend_getUserAccount.json`: Represents a borrower with mixed collateral.

Import these files into your design tool or front-end mocks so that UI work can
proceed without requiring a running node.
