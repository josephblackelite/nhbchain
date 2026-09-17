# End-to-End Flow Validation Guide

End-to-end (E2E) testing verifies that user-critical workflows continue to operate once code and configuration changes land. This guide covers environment setup, execution, and interpreting the resulting telemetry.

## Core flows

| Flow | Components | Success signals |
| --- | --- | --- |
| Retail payment | Wallet → Gateway → Consensus | Transaction confirmed on-chain, receipt in gateway logs, customer notification emitted. |
| OTC voucher mint | Back-office → Swap RPC → Reconciler | Voucher minted on-chain, reconciler exports CSV/Parquet with no anomalies. |
| Validator join | Bootstrapper → Consensus node → Monitoring | Node catches up to latest height, shares state snapshot, alerts remain green. |
| Bridge transfer | External chain → Relayer → nhbchain | Proof accepted, assets minted/burned with finality metrics recorded. |

## Environment preparation

1. **Select a target network.** Use devnet for rapid iteration, staging for production-like checks, and mainnet only for observability audits.
2. **Provision data.** Seed accounts with known balances, configure OTC vouchers, and prepare validator keys as needed.
3. **Enable tracing and metrics.** Ensure Jaeger/Tempo, Prometheus, and log aggregation are active so failures can be diagnosed.
4. **Snapshot baseline dashboards.** Capture pre-run metrics for comparison.

## Running test scenarios

- Execute scripted flows using `make e2e` or service-specific CLI helpers (e.g., `cmd/nhbchain` for validator actions).
- For manual runs, follow the runbooks referenced in `docs/runbooks/` and record every command executed.
- Tag test transactions with unique memos or metadata so they can be located in block explorers and logs.

## Observability checklist

- **Logs:** Confirm expected log entries appear in gateway, consensus, and relayer services. Flag errors and warnings for follow-up.
- **Metrics:** Compare latency, error rate, and throughput counters to baseline values. Note any deviations larger than 10%.
- **Traces:** Inspect distributed traces for long spans (>2x baseline) or missing instrumentation.

## Failure handling

1. Capture logs, metrics screenshots, and trace IDs immediately.
2. File an incident or bug ticket with reproduction steps and environment details.
3. Coordinate with service owners to triage and verify fixes.

## Exit criteria

- All core flows complete without critical alerts or untriaged regressions.
- Evidence (logs, metrics, traces) is archived in the audit artifact store.
- Runbooks are updated if manual steps changed or new remediation procedures were required.
