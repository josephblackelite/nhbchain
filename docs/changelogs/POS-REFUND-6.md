# POS-REFUND-6 – Refund linkage & over-refund guard

## Summary

* Added `refundOf` metadata to transaction envelopes and client bindings.
* Introduced a refund ledger that tracks the origin transaction amount, all
  linked refunds, and the cumulative refunded total.
* Enforced validation that prevents cumulative refunds from exceeding the
  recorded origin amount.
* Defined a read-only `tx.v1.Query/RefundThread` RPC (`proto/tx/tx.proto`, with
  generated stubs) for exploring the refund history associated with a
  transaction. No server implementation is registered, so the RPC is **not
  reachable today** (see `docs/specs/refunds.md`); the backing data,
  `RefundLedger.Thread` (`core/state/refund_ledger.go`), is implemented but nothing outside
  its unit test calls it. Transaction execution uses the ledger through `bank.ValidateRefund`
  (`core/state_transition.go`), `bank.RecordOrigin` and `bank.RecordRefund` (which calls
  `RefundLedger.ApplyRefund`; `native/bank/transfer.go`).
* Documented the on-chain flows, client expectations, and example UX in
  `docs/specs/refunds.md`.

## Testing

* `go test ./core/state -run RefundLedger` – verifies refund threading visibility
  and rejects over-refunds.
