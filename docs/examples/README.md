# Example Applications

This guide shows how to run the NHB Chain example applications against the public testnet. Each project demonstrates authentication, idempotency, and tracing best practices from the SDKs.

## Prerequisites

- Node.js 18+ and Go 1.21+
- Docker and Docker Compose
- NHB Chain API credentials with least-privilege scopes for the relevant product areas
- Access to the testnet RPC endpoint (`https://rpc.testnet.nhbchain.xyz`)

## Repository Layout

- `examples/merchant-js`: browser checkout flow using the JS SDK
- `examples/escrow-js`: custodial escrow workflow with automated release
- `examples/swap-js`: token swap integration demonstrating idempotent order placement
- `examples/merchant-go`: backend service for merchant settlement
- `examples/escrow-go`: escrow orchestration microservice
- `examples/swap-go`: Go-based swap engine with Prometheus metrics

## Environment Setup

1. Clone the repository and install dependencies:

   ```bash
   git clone https://github.com/nhbchain/examples.git
   cd examples
   npm install && go mod tidy
   ```

2. Copy the sample environment file and configure secrets:

   ```bash
   cp .env.example .env
   # Set NHB_API_KEY, NHB_API_SECRET, NHB_ENV=testnet
   ```

3. Start shared services (Redis, Postgres, mock webhooks):

   ```bash
   docker compose up -d
   ```

## Running the JavaScript Apps

```bash
cd examples/merchant-js
npm run dev
```

- Visit `http://localhost:3000` for the merchant checkout demo.
- Use the provided test cards; the app calls the RPC with HMAC auth and logs trace IDs to the console.

For the escrow and swap apps, run `npm run start` in their respective directories. Each app exports Prometheus metrics at `http://localhost:9464/metrics`.

## Running the Go Apps

```bash
cd examples/merchant-go
NHB_API_KEY=... NHB_API_SECRET=... go run ./cmd/server
```

- Health endpoint: `GET /healthz`
- Metrics endpoint: `GET /metrics`
- Structured logs are emitted to stdout in JSON format.

For automated tests:

```bash
go test ./...
```

## Observability Hooks

- All apps emit traces via OpenTelemetry; configure the OTLP endpoint with `OTEL_EXPORTER_OTLP_ENDPOINT`.
- Logs include redaction utilities to avoid leaking secrets.
- Alerts can be simulated by running the `scripts/fire-alerts.sh` script to raise sample PagerDuty events.

## Troubleshooting

- Verify API credentials with `curl https://rpc.testnet.nhbchain.xyz/healthz`.
- If HMAC verification fails, ensure system clocks are in sync (use `chronyd` or `ntpd`).
- For persistent errors, capture trace IDs from logs and inspect in Grafana Tempo.

## Next Steps

- Deploy the examples to staging to validate CI/CD pipelines.
- Integrate dashboards from `/docs/ops/observability.md` to monitor sample traffic.
- Contribute improvements back via pull requests following the standard review checklist.
