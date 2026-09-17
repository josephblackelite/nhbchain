# POTSO Reward Notifications

Settlement events are emitted by the node every time a reward becomes claimable or is paid. Operators typically forward these
events to downstream systems (email, CRM, treasury bots) via webhooks.

## Event Types

| Event | Mode | Attributes |
|-------|------|------------|
| `potso.reward.ready` | claim mode only | `epoch`, `address`, `amount`, `mode` (always `claim`) |
| `potso.reward.paid`  | both modes      | `epoch`, `address`, `amount`, `mode` (`auto` or `claim`) |

Events are queued inside the state processor and accessible through the existing event streaming interfaces. Each attribute is
encoded as a string. In claim mode the `ready` event is emitted at epoch close while `paid` fires after a successful
`potso_reward_claim` call.

## Webhook Envelope

Downstream services commonly deliver events using an HTTP POST with the following envelope:

```json
{
  "type": "potso.reward.ready",
  "emittedAt": "2024-03-18T12:30:00Z",
  "data": {
    "epoch": 199,
    "address": "nhb1examplewinner...",
    "amount": "920000000000000000000",
    "mode": "claim"
  },
  "signature": "sha256=..."
}
```

* `type` mirrors the on-chain event type.
* `emittedAt` should be populated by the webhook dispatcher using the node’s wall clock.
* `signature` is an HMAC (recommended) or detached signature that allows receivers to verify authenticity.

## Retry & Backoff

1. Use exponential backoff with jitter. A starting delay of 5 seconds doubling up to 10 minutes works well.
2. Keep a delivery log including the HTTP status and error body for each attempt.
3. After a maximum number of attempts (e.g. 15) escalate to operators but retain the event in a dead-letter queue for manual
   replay.

## Implementing Signature Verification

* Choose a shared secret (`POTSO_WEBHOOK_SECRET`).
* The dispatcher signs `HMAC_SHA256(secret, type + "|" + emittedAt + "|" + base64(data_json))`.
* Receivers recompute the HMAC and compare using a constant-time check.
* Rotate the secret periodically and keep the previous secret available during the transition window to avoid losing events.

## Operational Recommendations

* **Idempotency:** include the tuple `(type, epoch, address)` in the webhook payload. Receivers should treat this as an
  idempotency key to avoid double-processing.
* **Alerting:** trigger alerts when ready events remain unclaimed past SLA thresholds. History pagination exposes which entries
  are still pending.
* **Auditing:** store webhook payloads (after signature verification) alongside the CSV exports to maintain a full audit trail.
* **Testing:** use `nhb-cli potso reward claim` and `potso_export_epoch` against a devnet to validate webhook consumers before
  flipping the production node to claim mode.

These guidelines keep notification pipelines resilient and verifiable while delivering real-time visibility into reward
settlement.
