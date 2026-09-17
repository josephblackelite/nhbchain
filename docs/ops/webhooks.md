# Escrow Gateway Webhook Queue Operations

The escrow gateway protects itself from webhook floods by bounding in-memory queues and expiring stale events. This document describes the operational knobs and telemetry relevant to that pipeline.

## Configuration

The queue exposes three environment variables that can be tuned without redeploying code:

| Variable | Default | Description |
| --- | --- | --- |
| `ESCROW_GATEWAY_QUEUE_CAP` | `1024` | Maximum number of pending webhook tasks. When the queue is full, the oldest task is dropped. |
| `ESCROW_GATEWAY_QUEUE_HISTORY` | `256` | Number of recent webhook events retained for inspection via diagnostics and tests. Oldest entries are discarded once the buffer is full. |
| `ESCROW_GATEWAY_QUEUE_TTL` | `15m` | Per-item time-to-live. Tasks and history entries older than this duration are evicted before delivery. |

All values must be positive. TTL uses Go duration syntax (for example `30s`, `5m`).

## Metrics

`nhb.escrow.webhooks.dropped` (counter) increments whenever a webhook is discarded. The counter includes a `reason` attribute with the following values:

- `overflow` – queue was at capacity and the oldest task was removed to make room for a new event.
- `history_overflow` – the history buffer reached its limit and dropped the oldest recorded event.
- `ttl` – a task aged past the configured TTL before it could be delivered.
- `history_ttl` – a history entry aged past the configured TTL.

Alerting should focus on non-zero `overflow` and `ttl` reasons, which indicate sustained pressure or delivery stalls.
