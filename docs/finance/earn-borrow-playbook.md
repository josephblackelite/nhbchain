# NHBCoin Finance Playbook

Date: 2026-04-15

This note explains how the `Finance > Earn` and `Finance > Borrow` flows are
intended to work in NHBCoin today.

## 1. Earn Flow

The earn pool is the NHB liquidity source for the lending market.

- Users supply `NHB` into the lending pool.
- That NHB becomes pool liquidity.
- The protocol records the supplied balance for the lender.
- Borrowers draw `NHB` from that shared pool.
- Interest paid by borrowers is the source of lender yield.

In practical terms:

- `Earn` users are liquidity providers.
- Their supplied `NHB` is the inventory that later funds borrows.
- If no one has supplied NHB, the market has no NHB to lend out.

## 2. Borrow Flow

The borrow side uses `ZNHB` as collateral to borrow `NHB`.

- User deposits `ZNHB` as collateral.
- The system values that collateral in USD using the `ZNHB` oracle price.
- The market applies the configured maximum loan-to-value (`MaxLTV`).
- The user can borrow NHB up to that limit, subject to available pool liquidity.

Current production parameters:

- `MaxLTV`: `75%`
- `Liquidation threshold`: `85%`

Example:

- User deposits `50,000 ZNHB`
- Oracle price is `$0.05`
- Collateral value = `$2,500`
- With `75%` MaxLTV, maximum borrowable NHB value = `$1,875`

So borrowing `250 NHB` should be comfortably inside the limit when both:

- the collateral is recorded correctly
- the pool has enough NHB liquidity

## 3. Where The Borrowed NHB Comes From

Borrowed `NHB` comes from the `Earn` pool.

That means:

- Lenders supply NHB
- Borrowers take NHB from that supplied liquidity
- Repayments and interest return value to the pool

This is why earn and borrow are two sides of the same market.

## 4. Why Persistence Matters

For the user experience to stay trustworthy after reloads and restarts, three
things must remain in sync:

- pool liquidity
- borrower collateral / debt
- activity history

The portal now persists lending activity history in Prisma so:

- deposits
- collateral moves
- borrows
- repayments

can be reconstructed for reconciliation and UI history.

## 5. Economics Summary

- `Earn`: supply NHB, earn from borrower interest
- `Borrow`: lock ZNHB, borrow NHB against it
- `Oracle`: values ZNHB collateral in USD terms
- `Risk engine`: enforces MaxLTV and liquidation thresholds
- `Pool liquidity`: determines whether a borrow can actually be funded

## 6. Operational Rule

If a user shows valid collateral but still cannot borrow, check these in order:

1. the collateral position is present
2. the pool liquidity is present
3. the risk parameters are loaded (`MaxLTV`, liquidation threshold)
4. the account/activity state survived reload or restart
