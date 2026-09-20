# End-to-End Flow Validation Guide

This page describes the end-to-end tests that exist in the repository, what each one exercises, and what it does not.

## What runs

| Command | What it does |
| --- | --- |
| `make audit:e2e` | Runs `go test -json -run TestAuditSmokePlan ./tests/e2e/...`, then `scripts/audit/run_phase.sh e2e ops/audit/e2e.yaml artifacts/e2e --compose deploy/compose/docker-compose.audit-e2e.yaml`. Output goes to `artifacts/e2e/` and `logs/`. |
| `make bugcheck-gateway` | Runs `go test -run TestEndToEndFinancialFlows ./tests/e2e`. |
| `go test ./tests/e2e/...` | Runs every test in the package, listed below. |

There is no `make e2e` target.

`TestAuditSmokePlan` (`tests/e2e/audit_smoke_test.go`) only reads `ops/audit/e2e.yaml` and fails if it defines no checks. `scripts/audit/run_phase.sh` calls `tools/audit`, which records each check from the YAML file with the status `pending`, hashes the config, and parses the compose file. It does not execute the checks or start the compose stack.

## Tests in `tests/e2e`

| Test | Implementation | Coverage |
| --- | --- | --- |
| `TestEndToEndFinancialFlows` | in-process HTTP simulation (`tests/support/cluster`) | Through a simulated gateway: lending supply (with an idempotent replay), borrow, repay; swap mint and redeem; governance proposal, vote and apply; then a consensus snapshot check. |
| `TestLendingRPCEndpoints` | real `core.Node` and `rpc.Server` with an in-memory database, HS256 JWT | Lending JSON-RPC endpoints. |
| `TestPotsoTask3Determinism` | real POTSO evidence, penalty and reward packages, two pipeline instances per seed (42, 1337, 9001) | Both instances must end in identical snapshots, duplicate deliveries must occur, and results are compared to golden files (`UPDATE_POTSO_GOLDEN=1` rewrites them). |

`tests/support/cluster` imports no `nhbchain` package. Its "consensusd", "p2pd", "lendingd", "swapd", "governd" and "gateway" services are handlers over an in-memory state on loopback ports, not the real binaries or chain code. The tests in `tests/chaos` (service kill and restart; run them with `go test ./tests/chaos/...`, because `make audit:chaos` does not run them) and `tests/perf` (`BenchmarkConsensusFinalityLatency`) run against the same simulation, so a pass there says nothing about the real node's consensus, lending, swap or governance logic.

## Running against a real network

The repository contains no scripted end-to-end suite that drives a real node cluster. For manual checks against a running node:

1. Choose the target network and note its chain ID and RPC endpoint.
2. Submit transactions with the CLI or SDKs and record the transaction hashes.
3. Confirm each result with the JSON-RPC read methods (for example `nhb_getTransactionReceipt`).
4. Keep the commands, hashes and outputs with the audit record.

## Exit criteria

- The tests above pass on the commit under review.
- Anything that needed manual steps is written down so it can be repeated.
