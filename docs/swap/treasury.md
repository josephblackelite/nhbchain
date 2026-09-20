# Swap Burn Receipts and Reconciliation

This page covers the burn-receipt ledger (`native/swap/redeem.go`) and the
reconciliation events. Read the status notes: the write path for burn
receipts is not reachable through any live interface.

## Burn receipts

A `BurnReceipt` has these fields: `ReceiptID`, `ProviderTxID`, `Token`,
`AmountWei`, `Burner`, `RedeemReference`, `BurnTxHash`, `TreasuryTxID`,
`VoucherIDs`, `ObservedAt`, `Notes`. Receipts are stored under
`swap/burn/<receiptId>` with an index at `swap/burn/index`. A receipt ID can
be stored only once (`burn ledger: receipt <id> already exists`).

`swap_burn_list` (JWT required) params: `startTs`, `endTs`, optional `cursor`,
optional `limit` (default 50). Result: `{"receipts": [...], "nextCursor": "..."}`.
Each receipt is rendered with `receiptId`, `providerTxId`, `token`,
`amountWei`, `observedAt`, and, when set, `burner`, `redeemRef`, `burnTx`,
`treasuryTx`, `voucherIds`, `notes` (`rpc/swap_admin_handlers.go`
`formatBurnReceipt`).

## `SwapRecordBurn`

`Node.SwapRecordBurn` (`core/node.go` line 8374):

* fails when the `swap` module is paused, or when `receiptId` is empty;
* writes the receipt to the burn ledger;
* sets each listed voucher's status to `reconciled`;
* appends these events:
  * `swap.burn.recorded` - `receiptId`, `providerTxId`, `token`, `amountWei`, and when set `burnTx`, `treasuryTx`, `vouchers`, `observedAt`;
  * `swap.treasury.reconciled` - `vouchers`, `receiptId`, `observedAt` (only when the receipt lists vouchers);
  * `swap.redeem.proof` - `receiptId`, `providerTxId`, `vouchers`, `priceProofIds`, `observedAt` (only when at least one listed voucher has a price proof ID).

**Status:** the only callers of `SwapRecordBurn` are unit tests. It is not
exposed through an RPC method, transaction type, or CLI command. It also
mutates state directly on the node rather than through a consensus
transaction, so in its current form it would not be replicated to other
validators. `swap_burn_list` can only return receipts that were written some
other way.

## Reconciliation of vouchers

The reachable reconciliation path is `swap_markReconciled`, a signed
`TxTypeSwapMarkReconciled` transaction that sets vouchers to `reconciled` and
emits `swap.treasury.reconciled` ([admin.md](admin.md#swap_markreconciled)).
A reconciled voucher can no longer be reversed.

## Redemption (swap-out) records

Separate from burn receipts, `TxTypeRedeemNHB` stores a redemption request
and `TxTypeAttestRedemption` settles it. Events:

* `swap.redeem.requested` - emitted on redeem.
* `swap.redeem.attested` - emitted on attestation.
* `swap.redeem.refunded` - emitted when a `failed` attestation re-credits the burned NHB.

Pending requests are listed with `swap_listPendingRedemptions`
([README.md](README.md#rpc-methods)). Attestation requires
`ROLE_SWAP_PAYOUT_ATTESTOR`; a `paid` attestation requires a non-empty
`payoutReference`; only `pending` requests can be attested
(`core/state_transition.go` `applyAttestRedemption`).
