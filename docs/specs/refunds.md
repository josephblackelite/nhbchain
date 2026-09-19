# Refund linkage

NHB transfers can be linked to an earlier transfer so the total refunded never
exceeds what the original transfer moved. Code: `core/state_transition.go`
(`applyEvmTransaction`), `native/bank/transfer.go`, `core/state/refund_ledger.go`.

## Transaction field

`refundOf` (string) is a field of every `types.Transaction` (`refund_of` in the
gRPC `TxEnvelope`), covered by the signed hash. It holds a 32-byte transaction
hash, hex-encoded with or without `0x`, exactly 64 hex characters
(`bank.ParseTxHash`). It is only acted on for `TxTypeTransfer` (NHB transfers).
ZNHB transfers and other types ignore it and record nothing in the ledger.

## Ledger behaviour

For an NHB transfer (`TxTypeTransfer`), the state processor uses the
transaction's hash (`Transaction.Hash`):

* `refundOf` empty: the transfer is an origin. `RecordOrigin` stores its `value`
  as `originAmount` with the block timestamp. Origin amounts must be positive; a
  zero-value origin is not recorded. Recording the same hash again with a
  different amount is an error.
* `refundOf` set: before moving funds, `ValidateRefund` looks up the origin. It
  fails when the origin is not in the ledger (`refund: origin <hash> not found`),
  when the refund amount is not positive, or when `cumulativeRefunded + value >
  originAmount` (`refund: cumulative refunds X exceed origin amount Y`). On
  success `ApplyRefund` appends `{refundTx, amount, timestamp}` and increases
  `cumulativeRefunded`. A refund transaction is not itself recorded as an origin.

Failing the check rejects the transaction; no state is committed for it. The
ledger is stored under the key prefix `refund/thread/<origin hash>`:

| Field | Meaning |
| --- | --- |
| `originAmount` | `value` of the origin transfer. |
| `originTimestamp` | Block timestamp of the origin transfer (unix seconds). |
| `cumulativeRefunded` | Sum of recorded refunds. |
| `refunds[]` | `refundTx`, `amount`, `timestamp` in order applied. |

The refund transfer itself is an ordinary transfer from whoever signs it to the
recipient it names; the ledger only limits the running total.

## Reading the ledger

`proto/tx/tx.proto` defines `tx.v1.Query/RefundThread` (request `origin_tx`;
response `origin_tx`, `origin_amount`, `cumulative_refunded`, `origin_timestamp`,
`refunds[]` with `refund_tx`, `amount`, `timestamp`). No server in this repository
registers that service, so the RPC is not reachable; there is no JSON-RPC method
that returns the ledger.

## Example

1. Origin: `refundOf` omitted, `value` 1000. Ledger: `originAmount` 1000.
2. Refund A: `refundOf = <origin hash>`, `value` 400. `cumulativeRefunded` 400.
3. Refund B: `value` 600. `cumulativeRefunded` 1000.
4. Refund C with any positive `value` is rejected.

Covered by `TestRefundLedgerRecordAndThread` and `TestRefundLedgerOverRefund` in
`core/state/refund_ledger_test.go`.
