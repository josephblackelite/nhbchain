# Go SDK Guide

Use the NHB Chain Go SDK to build backend services with robust authentication, tracing, and idempotency support.

## Installation

```bash
go get github.com/nhbchain/go-sdk
```

Initialize the client in your application:

```go
package main

import (
    "context"
    "log"

    nhb "github.com/nhbchain/go-sdk"
)

func main() {
    client, err := nhb.NewClient(nhb.Config{
        Endpoint:  "https://rpc.testnet.nhbcoin.net",
        APIKey:    getenv("NHB_API_KEY"),
        APISecret: getenv("NHB_API_SECRET"),
    })
    if err != nil {
        log.Fatal(err)
    }

    account, err := client.Accounts.Get(context.Background(), "merchant-123")
    if err != nil {
        log.Fatal(err)
    }

    log.Printf("balance: %s", account.Balance)
}
```

## Authentication & Signing

- Requests use HMAC-SHA256 signatures computed over the HTTP method, path, timestamp, and payload.
- Signatures are attached via headers `X-NHB-APIKEY`, `X-NHB-SIGNATURE`, and `X-NHB-TIMESTAMP`.
- The SDK enforces a 5-minute timestamp skew window.

## Idempotency Helpers

```go
key := nhb.IdempotencyKey{
    Namespace: "merchant-123",
    Reference: orderID,
}.String()

payment, err := client.Payments.Create(ctx, nhb.CreatePaymentRequest{
    Amount:         "25.00",
    Currency:       "USD",
    IdempotencyKey: key,
})
```

The helper normalizes whitespace, truncates long references, and includes a SHA-256 checksum.

## Retries & Timeouts

- Default timeout: 10 seconds per request; override with `client.WithTimeout()`.
- Retries: exponential backoff with jitter, max 4 attempts on retryable errors (`context.DeadlineExceeded`, `HTTP 429`, `HTTP >= 500`).
- Hooks: implement `RetryObserver` to emit custom metrics.

## Tracing Integration

The SDK integrates with OpenTelemetry:

```go
import "go.opentelemetry.io/otel"

tracer := otel.Tracer("nhbchain-go-sdk")
ctx, span := tracer.Start(ctx, "payments.create")
defer span.End()

_, err := client.Payments.Create(ctx, req)
if err != nil {
    span.RecordError(err)
}
```

Trace context is propagated downstream so service spans align with RPC traces in Tempo.

## Logging & Redaction

- Structured logs via `zap` or `log/slog` include `trace_id`, `request_id`, and sanitized payload excerpts.
- Enable payload redaction with `client.SetRedactor(nhb.DefaultRedactor())` to mask PANs and secrets.

## Example Applications

Reference the Go sample apps for end-to-end flows:

- `examples/merchant-go`
- `examples/escrow-go`
- `examples/swap-go`

Each example includes Docker Compose files to start dependencies and run against testnet.

## Testing & CI

```bash
NHB_API_KEY=... NHB_API_SECRET=... go test ./...
```

- Use the provided `httptest` mocks under `sdk/testutil` for offline tests.
- Capture coverage reports and ship metrics to Prometheus using the `sdk/metrics` helper.
- gRPC clients in `nhbchain/sdk` now require explicit dial options. Connections default to TLS using the host certificate pool; pass `consensus.WithInsecure()` (or the equivalent helper in other SDK packages) when developing against local plaintext endpoints.

## Security Practices

- Store secrets in environment variables or KMS-managed secrets, never in source control.
- Rotate API credentials at least quarterly.
- Monitor `nhb_rpc_auth_failure_total` and `nhb_sdk_idempotency_conflict_total` metrics for anomalies.
