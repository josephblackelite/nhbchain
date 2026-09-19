# Stable Ledger Records

`native/swap/stable_store.go` (`StableStore`) defines an append-only record
set for USDC/USDT deposits and cash-outs. **Only one of its mutations is
reachable from a live transaction type, and that one cannot succeed without a
record that no live path creates.** Details per record below.

## Record types

| Record | Go type | Key |
|--------|---------|-----|
| Deposit voucher | `DepositVoucher` | `swap/stable/voucher/<invoice_id>` (index at `swap/stable/voucher/index`) |
| Cash-out intent | `CashOutIntent` | `swap/stable/intent/<intent_id>` |
| Escrow lock | `EscrowLock` | `swap/stable/escrow/<intent_id>` |
| Payout receipt | `PayoutReceipt` | `swap/stable/receipt/<intent_id>` |
| Treasury soft inventory | `TreasurySoftInventory` | `swap/stable/inventory/<ASSET>` |

Keys are in `native/swap/keys.go`. Amounts are stored as base-10 integer
strings. Stable assets are normalised to upper case and only `USDC` and `USDT`
are accepted (`StableAsset` in `native/swap/types.go`). Intent statuses are
`pending`, `settled`, `aborted`.

## Mutations

1. **`PutDepositVoucher`** - rejects a duplicate `InvoiceID`, requires positive
   stable and NHB amounts, stores the voucher and adds the stable amount to the
   soft inventory. Not reachable: `TxTypeSwapMint` (`0x11`) is stubbed to
   return "native on-chain swap mint is disabled" and the only callers of
   `PutDepositVoucher` are tests.
2. **`CreateCashOutIntent`** - requires enough soft inventory, stores a
   `pending` intent and an escrow lock (burn deferred). Not reachable:
   `TxTypeSwapBurn` (`0x12`) is stubbed the same way and the only callers are
   tests.
3. **`RecordPayoutReceipt`** - requires an existing `pending` intent, no
   existing receipt for it, decrements soft inventory, marks the escrow burned,
   reduces the NHB token supply by the intent's NHB amount and marks the
   intent `settled`; the receipt's asset and amounts must match the intent. This is wired to `TxTypeSwapPayoutReceipt`
   (`0x0F`): the sender must hold `ROLE_SWAP_PAYOUT_ATTESTOR` and the payload
   is a protobuf `Any` of `swap.v1.MsgPayoutReceipt`
   (`core/state_transition.go` `applySwapPayoutReceipt`). Because no live path
   creates an intent, it fails with `stable: intent <id> not found`.
4. **`AbortCashOutIntent`** - requires a `pending` intent whose escrow is not
   burned; sets the status to `aborted`. It has no caller in non-test code.
   The store only flips the status; it does not move any balance.

## Query surface

`GetDepositVoucher`, `GetCashOutIntent`, `GetEscrowLock`, `GetPayoutReceipt`
and `GetSoftInventory` are Go methods. The `Query*` messages in
`proto/swap/v1/stable.proto` have no service definition, so none of these is
reachable over RPC or gRPC. (`proto/swap/v1/swap.proto` defines a separate
`SwapService` for pools; it is unrelated to these records.)
