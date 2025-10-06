# POS-LIFECYCLE-5: Card-Style Payment Authorization

## Overview

This task introduces a card-style lifecycle for point-of-sale (POS) payments.
Merchants can lock ZapNHB from a payer, capture any amount up to the locked
value, or void the authorization to return funds. Expired authorizations are
voided automatically to guarantee balances are restored without manual
intervention.

Key additions include:

* A lifecycle engine that manages authorizations, captures, and voids while
  updating account balances atomically.
* Events emitted for each lifecycle milestone so downstream services can track
  payments in real time.
* New RPC messages that expose authorization, capture, and void entry points.
* Documentation of timing guarantees, error codes, and state transitions.

## Lifecycle flow

```mermaid
graph TD
    A[Authorize] -->|lock funds| B[Pending]
    B -->|Capture <= amount| C[Captured]
    B -->|Void| D[Voided]
    B -->|Expiry reached| E[Expired]
    C -->|Emit payments.captured| F[Merchant credited]
    D -->|Emit payments.voided| G[Payer refunded]
    E -->|Emit payments.voided(expired)| G
```

* **Authorize**: Locks the requested ZapNHB in `LockedZNHB` and records an
  authorization ID tied to the payer, merchant, amount, expiry, and optional
  `intent_ref`.
* **Capture**: Transfers any amount up to the authorized total to the merchant
  account. Remaining funds are returned to the payer in the same transaction.
* **Void**: Releases the entire lock back to the payer. This can be triggered
  manually via `MsgVoidPayment` or automatically when the expiry timestamp is
  reached.

## Message schema

| Message | Description |
| --- | --- |
| `MsgAuthorizePayment` | Locks ZapNHB on the payer account. Returns `authorization_id`. |
| `MsgCapturePayment` | Captures up to the locked amount, refunding any remainder. |
| `MsgVoidPayment` | Manually voids an authorization prior to capture. |

All amounts are decimal strings representing ZapNHB. Timestamps are UNIX seconds.

## State transitions

Authorizations are stored beneath the `pos/auth/<id>` namespace. Each record
tracks the total amount, captured portion, refunded portion, current status, and
metadata such as expiry and reason strings for voids. Per-payer counters ensure
authorization IDs remain unique while keeping them deterministic (hash of payer
address and nonce).

Account updates are atomic:

1. **Authorize**: `BalanceZNHB -= amount`; `LockedZNHB += amount`.
2. **Capture**: `LockedZNHB -= amount_authorized`; merchant `BalanceZNHB +=
   amount_captured`; payer `BalanceZNHB += remainder`.
3. **Void / Expire**: `LockedZNHB -= amount_authorized`; payer `BalanceZNHB +=
   amount_authorized`.

If any persistence step fails, balances are rolled back to the pre-operation
state before the error is returned.

## Timing and expiry

* Authorizations must specify an expiry strictly greater than the block
  timestamp that executes the authorize message.
* Captures after the expiry automatically void the authorization, emitting a
  `payments.voided` event with `expired=true` and returning the funds.
* Manual voids leave the authorization record in `voided` status so idempotent
  retries do not fail.

## Errors

| Error | Condition |
| --- | --- |
| `pos: lifecycle not initialised` | Lifecycle engine configured without state. |
| `pos: address required` | Payer or merchant address was missing/zeroed. |
| `pos: amount must be positive` | Amount was zero or negative. |
| `pos: insufficient balance` | Payer lacks enough ZapNHB to lock. |
| `pos: authorization expired` | Authorization expired before capture. |
| `pos: authorization already captured` | Attempted double capture. |
| `pos: authorization voided` | Capture attempted after a manual void. |
| `pos: authorization not found` | Unknown authorization ID. |

## Events

| Event | Payload |
| --- | --- |
| `payments.authorized` | `authorizationId`, `payer`, `merchant`, `amount`, `expiry`, optional `intentRef`. |
| `payments.captured` | `authorizationId`, `payer`, `merchant`, `capturedAmount`, `refundedAmount`. |
| `payments.voided` | `authorizationId`, `payer`, `merchant`, `refundedAmount`, `reason`, `expired`. |

Events are emitted even when voided automatically so settlement systems can
resolve outstanding holds.

## Integration points

* **RPC**: The new messages are exposed via the `pos.v1.Tx` service defined in
  `proto/pos/tx.proto`.
* **Testing**: Unit tests cover partial capture, double-capture rejection, and
  automatic expiry handling.
* **Telemetry**: Existing payment processors can subscribe to the new event
  types to synchronize state with NHBChain.
