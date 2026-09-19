# POS intent field reference

Where each intent field appears. Semantics and enforcement rules are in
[NHB Pay point-of-sale intents](./nhb-pay.md); mempool priority in
[POS quality of service](./pos-qos.md).

| Concept | `nhb_sendTransaction` JSON | `TxEnvelope` proto | Go field (`types.Transaction`) | Enforced by the chain |
| --- | --- | --- | --- | --- |
| Intent reference | `intentRef` (base64) | `intent_ref` | `IntentRef` | Single use; 1 to 64 bytes in the registry; consumed on successful apply. |
| Deadline | `intentExpiry` | `intent_expiry` | `IntentExpiry` | Must be non-zero and after block time when `intentRef` is set; stored expiry clamped to block time + 24 hours. |
| Merchant | `merchantAddr` | `merchant_addr` | `MerchantAddress` | Not validated as an address; selects the fee domain; echoed in `payments.intent_consumed`. |
| Device | `deviceId` | `device_id` | `DeviceID` | Opaque; echoed in `payments.intent_consumed`. |
| Refund link | `refundOf` | `refund_of` | `RefundOf` | Only for `TxTypeTransfer`; see [refunds](./refunds.md). |
| Gas sponsor | `paymaster` (base64, 20 bytes) + `paymasterR/S/V` | not carried by `TxEnvelope`; `consensus.v1.Transaction.paymaster` and `paymaster_r/s/v` | `Paymaster`, `PaymasterR/S/V` | Signature by the paymaster address over the transaction hash. |

Not on-chain: `amount` (the amount is `value`, a wei integer) and `currency` (no
such field exists). These appear only in the URI convention and the proposed NFC
record.

Events: `payments.intent_consumed` carries `intentRef`, `txHash` and, when set,
`merchantAddr` and `deviceId`. The authorize, capture and void lifecycle events are
in [POS lifecycle](./pos-lifecycle.md). Finality updates keyed by `intentRef` are
in [POS realtime](../api/pos-realtime.md).
