# Lending Developer Guide

This guide outlines the recommended flow for building a lending experience on
NHBChain using the JSON-RPC endpoints exposed by the node.

## 1. Discover Risk Configuration

- Call [`lending_getMarket`](rpc-api.md#lending_getmarket) during startup to
  cache the latest pool totals and risk parameters. Surface values such as
  `maxLTV`, `liquidationThreshold`, and `liquidationBonus` in tooltips so users
  understand protocol limits.
- Repeat the call periodically to refresh supply and borrow totals displayed in
  the UI.

## 2. Authenticate the User

- Prompt the user to connect their NHBChain wallet.
- Retrieve the bearer token required by the node operator and attach it to every
  state-changing request:

  ```http
  Authorization: Bearer <token>
  ```

  Omit the header when invoking read-only methods such as
  `lending_getMarket`.

## 3. Load the Account Snapshot

- Fetch the lending position with [`lending_getUserAccount`](rpc-api.md#lending_getuseraccount).
  Use the returned balances to pre-fill collateral toggles and outstanding debt
  amounts.
- If the endpoint returns a `404`, initialise the UI with zero balances and hide
  repayment controls until the user supplies or borrows for the first time.

## 4. Supply and Manage Collateral

1. Send a `lending_supplyNHB` request when the user deposits NHB liquidity.
2. Encourage the user to immediately lock funds with `lending_depositZNHB` so
   the position can back future borrows.
3. Allow partial withdrawals by combining `lending_withdrawZNHB` (to reduce
   collateral) and `lending_withdrawNHB` (to redeem LP shares) while keeping the
   projected health factor above 1.0.

## 5. Borrowing Workflow

- Compute projected health factors client-side before calling
  [`lending_borrowNHB`](rpc-api.md#lending_borrownhb). Block requests that would
  drive HF below 1.0 and warn users when the buffer is thin.
- If your application charges a fee, route the borrow through
  `lending_borrowNHBWithFee` and display the fee and net amount in the
  confirmation modal. Fees are specified in basis points.

## 6. Repayment and Upkeep

- Offer a one-click repay option using `lending_repayNHB` with the outstanding
  debt value. The engine automatically caps the amount to the borrower’s current
  debt.
- Refresh the account snapshot after each state change to keep the UI in sync.

## 7. Liquidation Monitoring

- Background services can poll `lending_getUserAccount` and alert borrowers when
  their collateral approaches the liquidation threshold.
- Liquidators can automate [`lending_liquidate`](rpc-api.md#lending_liquidate)
  calls. Ensure the liquidator wallet holds enough NHB to repay the targeted
  debt.

## Best Practices

- Treat the `txHash` returned by each action as an acknowledgement only. It is
  not persisted on-chain but can be logged for audit purposes.
- Clamp user-provided amounts to positive integers and validate input client
  side before hitting the node.
- Display the current supply and borrow indexes from `lending_getMarket` if you
  show yield metrics so users can understand accrual trends.
- Cache risk parameters but expose a manual refresh so advanced users can verify
  configuration after governance updates.

