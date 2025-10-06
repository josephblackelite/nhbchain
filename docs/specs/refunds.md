# Refund linkage and safeguards

The POS refund programme now enforces a strict linkage between refund
transactions and the original payment that funded them. This document captures
the on-chain mechanics, ledger semantics, and the API surfaces exposed to
clients.

## Transaction metadata

* All transactions now expose an optional `refundOf` metadata field. The field
  accepts the 32 byte transaction hash of the originating payment (hex encoded
  with or without the `0x` prefix).
* When `refundOf` is omitted the transaction is treated as the origin payment
  and its value is recorded as the maximum refundable amount.
* When `refundOf` is present the state processor resolves the ledger entry for
  the provided hash and rejects the transaction when the new refund would exceed
  the recorded origin amount.

## Refund ledger

Origin transactions and all linked refunds are recorded in the new
`RefundLedger` stored under `refund/thread/<origin-hash>`:

| Field | Description |
| --- | --- |
| `originAmount` | Value transferred by the origin transaction. |
| `originTimestamp` | Block timestamp (UTC seconds) of the origin transaction. |
| `cumulativeRefunded` | Sum of all recorded refund amounts. |
| `refunds[]` | Chronological entries containing `refundTx`, `amount` and `timestamp`. |

Ledger guarantees:

1. Origin amounts must be greater than zero.
2. Refund entries must reference an existing origin hash.
3. Each refund must keep `cumulativeRefunded <= originAmount`.

Attempts to exceed the origin amount abort the transaction before state is
committed, ensuring double-refunds cannot occur.

## Query service

`proto/tx/tx.proto` introduces a read-only `Query` service. The
`RefundThread(origin_tx)` RPC returns the ledger view needed to surface refund
threads in wallets and dashboards.

**Response fields:**

* `origin_tx` – the referenced transaction hash.
* `origin_amount` – the amount locked by the origin payment.
* `cumulative_refunded` – total refunds applied so far.
* `refunds[]` – each linked refund with `refund_tx`, `amount`, and `timestamp`.

Amounts are string encoded in both protobuf and the TypeScript client bindings
to avoid JSON precision loss.

## UX guidance

* Wallets should prompt the operator for the original payment hash when
  initiating a refund.
* The refund summary page should fetch the thread and surface both the remaining
  refundable balance and the list of processed refunds.
* Attempts to exceed the refundable balance should be blocked client-side but
  the node will enforce the invariant even if a malicious client attempts to
  bypass the warning.

## Example flow

1. Origin payment: `refundOf` omitted. Ledger records a refundable balance of
   `1000` units.
2. Refund transaction A: `refundOf = <origin-hash>`, amount `400`. Ledger links
   the refund and sets the cumulative tally to `400`.
3. Refund transaction B: `refundOf = <origin-hash>`, amount `600`. Ledger links
   the refund, cumulative tally becomes `1000`.
4. Refund transaction C: any amount > `0` would be rejected because the origin
   has already been fully refunded.

This behaviour is captured by the unit test suite (`refund threading visible`
and `over-refund rejected`).
