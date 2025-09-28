# Stable Ledger Records

The swap module maintains an append-only ledger for USD-backed stablecoin flows. The
state is split across five record types that map to the operational lifecycle of a
deposit or redemption:

- **DepositVoucher** – immutable record describing a USDC/USDT deposit that minted NHB.
- **CashOutIntent** – user's request to redeem NHB for stablecoins, paired with an escrow lock.
- **EscrowLock** – the NHB amount isolated on-chain until a payout receipt is observed.
- **PayoutReceipt** – attestation emitted by treasury operators after the fiat leg settles.
- **TreasurySoftInventory** – running aggregate of deposits minus payouts per asset.

## Data model

| Record | Key | Notes |
|--------|-----|-------|
| DepositVoucher | `swap/stable/voucher/<invoice_id>` | Enforces invoice idempotency and stores metadata used for reconciliation. |
| CashOutIntent | `swap/stable/intent/<intent_id>` | Captures account, requested asset amounts, and status transitions (`pending`, `settled`, `aborted`). |
| EscrowLock | `swap/stable/escrow/<intent_id>` | Tracks the escrowed NHB amount and whether it has been burned. |
| PayoutReceipt | `swap/stable/receipt/<intent_id>` | One-to-one with the intent; once present the escrow is burned and the intent is settled. |
| TreasurySoftInventory | `swap/stable/inventory/<asset>` | Maintains deposit/payout totals, updated timestamp, and current balance. |

All amounts are stored as 10-base integer strings. Stable assets are normalised to upper
case (currently `USDC` and `USDT`) and rejected if unsupported.

## Events & mutations

1. **Voucher mint** (`MsgMintDepositVoucher`)
   - Validates the invoice is new, persists the `DepositVoucher`, and appends it to the
     stable voucher index.
   - Adds the deposit amount to the treasury soft inventory for the asset.

2. **Cash-out intent** (`MsgCreateCashOutIntent`)
   - Requires sufficient soft inventory balance before locking NHB in escrow.
   - Stores the pending `CashOutIntent` and associated `EscrowLock` (burn deferred).

3. **Payout receipt** (`MsgPayoutReceipt`)
   - Verifies the receipt matches the intent, decrements soft inventory, and burns the
     escrowed NHB.
   - Persists the `PayoutReceipt` and marks the intent as `settled` with a timestamp.

4. **Abort** (`MsgAbortCashOutIntent`)
   - Releases the escrow and marks the intent `aborted` (implementation TBD).

## Invariants

- Deposit vouchers are append-only; attempting to mint the same invoice fails.
- Cash-out intents must settle to exactly the requested amounts (both NHB and stable).
- Soft inventory balance can never go negative; payouts are rejected if insufficient
  deposits exist.
- Escrow locks burn exactly once after a matching payout receipt, satisfying
  "burn-after-settle" requirements.
- Stable assets are validated for every mutation to avoid unexpected buckets.

The keeper exposes query endpoints (`QueryDepositVoucher`, `QueryCashOutIntent`,
`QuerySoftInventory`) to assist treasury dashboards and auditors with real-time views.
