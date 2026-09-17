# JavaScript & TypeScript SDK Guide

This guide explains how to install and use the NHB Chain JavaScript/TypeScript SDK to build secure, idempotent integrations.

## Installation

```bash
npm install @nhbchain/sdk
# or
yarn add @nhbchain/sdk
```

## Authentication

The SDK authenticates every RPC call with HMAC signatures.

```ts
import { NhbClient } from "@nhbchain/sdk";

const client = new NhbClient({
  endpoint: "https://rpc.testnet.nhbcoin.net",
  apiKey: process.env.NHB_API_KEY!,
  apiSecret: process.env.NHB_API_SECRET!,
});

const account = await client.accounts.get("merchant-123");
```

- API keys are issued with least privilege scopes (e.g., `payments:read`, `escrow:write`).
- Secrets should be stored in a vault and injected at runtime.

## HMAC Signing & Idempotency

The client signs requests using `HMAC-SHA256` with the `apiSecret` and injects headers:

- `X-NHB-APIKEY`
- `X-NHB-SIGNATURE`
- `X-NHB-TIMESTAMP`
- Optional `Idempotency-Key`

The SDK exposes helpers to generate idempotency keys tied to business identifiers:

```ts
import { createIdempotencyKey } from "@nhbchain/sdk/idempotency";

const key = createIdempotencyKey({
  namespace: "merchant-123",
  reference: order.id,
});

await client.payments.create({
  amount: "25.00",
  currency: "USD",
  idempotencyKey: key,
});
```

## Retries & Error Handling

- Automatic retries with exponential backoff (max 3 attempts) on transient errors (`429`, `5xx`).
- Circuit breaker trips when error rate exceeds 20% in 1 minute; manual reset via `client.resetBreaker()`.
- Structured errors contain `code`, `message`, and optional `traceId` for Grafana Tempo lookup.

## Tracing

The client propagates W3C trace headers and supports manual span creation:

```ts
const span = client.tracer.startSpan("checkout.createPayment");
await client.withSpan(span, () => client.payments.create({...}));
span.end();
```

## Example Apps

Clone the sample repositories and follow `/docs/examples/README.md` to run:

- `examples/merchant-js`
- `examples/escrow-js`
- `examples/swap-js`

## Testing Against Testnet

```ts
const health = await client.system.health();
console.log(health.status);
```

Run integration tests with `npm test` after configuring environment variables:

- `NHB_API_KEY`
- `NHB_API_SECRET`
- `NHB_ENV=testnet`

## Security Notes

- Rotate API secrets every 90 days.
- Redact sensitive fields when logging (`client.enableRedaction()` can help mask payloads).
- Monitor `nhb_rpc_auth_failure_total` in Grafana for brute-force attempts.
