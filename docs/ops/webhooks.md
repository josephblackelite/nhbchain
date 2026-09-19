# Escrow Gateway Webhook Queue Operations

The escrow gateway (`services/escrow-gateway`) bounds its in-memory webhook queue and expires stale events. This document describes the settings and telemetry for that queue (`config.go`, `webhook_queue.go`).

## Configuration

Three environment variables are read at startup (a restart is needed to change them):

| Variable | Default | Description |
| --- | --- | --- |
| `ESCROW_GATEWAY_QUEUE_CAP` | `1024` | Maximum number of pending webhook tasks. When the queue is full, the oldest task is dropped. |
| `ESCROW_GATEWAY_QUEUE_HISTORY` | `256` | Number of recent webhook events kept in the queue's history buffer (read by `WebhookQueue.Events()`). The oldest entry is discarded once the buffer is full. |
| `ESCROW_GATEWAY_QUEUE_TTL` | `15m` | Per-item time-to-live. Tasks and history entries older than this duration are evicted before delivery. |

A value that does not parse or is not positive stops the gateway at startup (for example `ESCROW_GATEWAY_QUEUE_CAP must be positive`). TTL uses Go duration syntax (for example `30s`, `5m`).

## Metrics

`nhb.escrow.webhooks.dropped` is an OpenTelemetry counter (meter `nhbchain/escrow-gateway`), exported over OTLP when the gateway's telemetry is configured. It increments whenever a webhook is discarded and carries a `reason` attribute:

- `overflow` – queue was at capacity and the oldest task was removed to make room for a new event.
- `history_overflow` – the history buffer reached its limit and dropped the oldest recorded event.
- `ttl` – a task aged past the configured TTL before it could be delivered.
- `history_ttl` – a history entry aged past the configured TTL.

`overflow` and `ttl` count tasks that never reach delivery; `history_overflow` and `history_ttl` only affect the history buffer.
