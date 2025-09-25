# Treasury Controls for Swap Flows

Treasury operations are now instrumented end-to-end: every burn, mint, and reconciliation step emits immutable ledger artifacts and RPC telemetry.

## Mint & burn receipts

- **Mint vouchers** persist TWAP metadata and the minter signature alongside fiat, rate, and oracle source information.
- **Burn receipts** capture the off-ramp lifecycle:
  - `receiptId`, `providerTxId`, `token`, `amountWei`, and the optional `burnTx` / `treasuryTx` identifiers.
  - `voucherIds` marks the mints that were offset by the burn.
  - `observedAt` timestamps the burn; it defaults to the ledger clock when omitted.
- The burn ledger can be queried via `swap_burn_list` and exported as CSV for reconciliation audits.

## Reconciliation flow

1. Off-ramp burns ZNHB from custody and publishes a burn receipt through the RPC admin API.
2. `SwapRecordBurn` writes the receipt to the burn ledger, marks linked vouchers as `reconciled`, and emits:
   - `swap.burn.recorded` – the authoritative off-ramp audit trail.
   - `swap.treasury.reconciled` – declaring the vouchers reconciled against treasury inventory.
3. Operators can also call `swap_markReconciled` (via RPC) for manual adjustments; the same event stream is produced.

## Policy guardrails

- Rate/velocity limits (`swap.risk`) continue to gate mint authorisations; exceedances emit limit alerts.
- Mint/burn receipts provide an immutable audit trail covering amount, rate, TWAP window, and counterparties.
- Reconciliation events are indexed by voucher identifiers, enabling downstream systems to assert one-to-one mint-to-burn coverage.

## Monitoring checklist

- Subscribe to `swap.burn.recorded` to track custody movements.
- Monitor `swap.treasury.reconciled` for missing voucher coverage.
- Export burn receipts periodically and compare against treasury statements.
