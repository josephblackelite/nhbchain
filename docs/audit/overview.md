# Audit Overview

This directory documents the audit tooling that exists in the repository. Start here to see which command covers which area, then use the per-topic pages.

## Tooling map

| Area | Command | Page |
| --- | --- | --- |
| Static analysis, vulnerability scan, protobuf lint and breaking-change check | `make audit:static`, `make bugcheck-static`, `make bugcheck-proto` | [static-analysis.md](./static-analysis.md) |
| Text-pattern security scan | `make audit:english` | [README.md](./README.md) |
| Fuzzing | `make bugcheck-fuzz` (`scripts/fuzz_all.sh`, one target at a time) or `go test -fuzz` for one target | [fuzzing.md](./fuzzing.md) |
| Unit and package tests | `make audit:tests` (`go test ./...` plus `go test ./...` in `sdk/`) | this page |
| Determinism and Byzantine-vote tests | `make audit:determinism` (`./tests/determinism/...`, `./tests/consensus/...`) | this page |
| End-to-end flows | `make audit:e2e`, `make bugcheck-gateway` | [e2e-flows.md](./e2e-flows.md) |
| Chaos | `go test ./tests/chaos/...` (`make audit:chaos` does not run these tests; it only records the two `pending` checks from `ops/audit/chaos.yaml`) | [e2e-flows.md](./e2e-flows.md) (the tests use the in-process simulation) |
| Network security tests | `make audit:netsec` (`./tests/netsec/...`) | this page |
| Performance | `make audit:perf` (`./tests/perf/...` benchmarks) | this page |
| Ledger and supply fixtures | `make audit:ledger`, `make audit:supply` (`./tests/ledger/...`) | this page |
| Configuration hashes | `make audit:config` (records `pending` checks and hashes `config.toml` and `config/prod.toml`) | this page |
| Documentation | `make audit:docs` | [docs-quality.md](./docs-quality.md) |
| Everything in one run | `make bugcheck` (`scripts/bugcheck.sh`) | [index.md](./index.md) |

## How the phase targets work

The `determinism`, `e2e`, `netsec`, `ledger`, `supply` and `perf` phase targets do two things: run a Go test package or benchmark (`go test -json` summarised by `scripts/audit/summarize_tests.py`, or `go test -bench` for `perf`) and then run `scripts/audit/run_phase.sh <phase> ops/audit/<phase>.yaml artifacts/<phase>`. `audit:e2e` runs only `TestAuditSmokePlan`, not the whole `tests/e2e` package. `audit:chaos` and `audit:config` do only the second step: no Go test runs, so `make audit:chaos` (and the `bugcheck-chaos` target that wraps it) never executes `tests/chaos`. `audit:docs` runs `go run ./tools/docs/verify.go` and then the second step.

The second step runs `go run ./tools/audit`, which reads the phase YAML, records every check listed there with the status `pending`, hashes the YAML and any `--hash` files, and writes `artifacts/<phase>/report.json` and `report.md`. It does not execute the checks named in the YAML. The YAML files in `ops/audit/` are plans and fixtures, and the pass/fail evidence comes from the Go tests. `tools/audit` exits with an error when a `--hash` file cannot be read (`hash %s: %v`, `tools/audit/main.go`).

`make audit:config` hashes `config.toml` and `config/prod.toml` (`Makefile`); `make audit:docs` hashes `docs/security/audit-readiness.md`. Both files exist in the repository.

`ops/audit/ledger.yaml` and `ops/audit/supply.yaml` contain sample records and numbers used by the ledger and supply tests, not chain data.

## Suggested order

1. Read the code and configuration in scope (`config/`, `core/`, `rpc/`, `p2p/`, `native/`, `services/`).
2. Run the static analysis and fuzzing steps.
3. Run the test phases, noting which ones use real packages and which use the in-process simulation.
4. Review the documentation against the code.
5. Track each finding to a fix and a regression test.

## Artifacts the commands produce

- `logs/` – tool and test logs.
- `artifacts/<phase>/` – JSON and Markdown phase summaries and test JSON.
- `audit/bugcheck-<timestamp>.md` and `artifacts/bugcheck-<timestamp>.json` – written by `scripts/bugcheck.sh` (see [index.md](./index.md)).
- `docs/audit/english-latest.md` – written by `make audit:english`.
