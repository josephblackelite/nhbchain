# Bugcheck history

The [latest bugcheck report](./latest.md) is automatically published after every pipeline run.

## Reading the report

Bugcheck gates must remain **PASS** to ship:

- **Static security** – staticcheck, go vet, golangci-lint, gosec, govulncheck.
- **Race tests** – `go test -race` across the full module graph.
- **Fuzzing** – coverage-guided fuzzing on critical state transitions.
- **Determinism** – multi-node determinism, state sync, and BFT safety checks.
- **Chaos** – container, network, and process fault injection resilience.
- **Performance** – proposer throughput and finality latency SLO verification.
- **Protobuf** – Buf lint and breaking-change contracts for all APIs.
- **Docs** – documentation linting, snippet verification, and example execution.

## Past runs

| Timestamp (UTC) | Report |
| --- | --- |
<!-- BUGCHECK_HISTORY -->
