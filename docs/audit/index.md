# Bugcheck history

[`latest.md`](./latest.md) holds the most recent report copied in by `scripts/publish_bugcheck.sh`. In CI, the `bugcheck-linux` job in `.github/workflows/ci.yml` runs `scripts/bugcheck.sh` and then `scripts/publish_bugcheck.sh`, and uploads `logs/`, `artifacts/bugcheck-*` and the `docs/audit/` files `index.md`, `latest.md`, `_meta.json` and `history/` as workflow artifacts. The publish step does not commit anything. It copies the newest report into `latest.md` and `history/`, adds a row to the table below and an entry to `_meta.json`, and copies `artifacts/bugcheck-<timestamp>.json` into `history/` when that file exists. The `bugcheck-linux` job also runs `scripts/verify_prod_config.sh` on `config/prod.toml` and on `config.toml` as a separate step before the bugcheck; both pass.

## What the bugcheck runs

`make bugcheck` (`scripts/bugcheck.sh`) runs these checks in order, each marked `critical`. Every check writes `logs/<id>.log`, and the run writes `audit/bugcheck-<timestamp>.md`, `artifacts/bugcheck-<timestamp>/summary.json` and a copy at `artifacts/bugcheck-<timestamp>.json`. The script exits 0 and prints "GREEN LIGHT" only when every check is `passed`; a `failed` or `missing` critical check makes the overall status `failed` and the exit status 1.

| Check id | Command | Covers |
| --- | --- | --- |
| `bugcheck-tools` | `make bugcheck-tools` | Installs `golangci-lint`, `staticcheck`, `gosec`, `govulncheck` and `buf` when missing. |
| `static-security` | `make bugcheck-static` | `golangci-lint`, `go vet`, `staticcheck`, `gosec`, `govulncheck`. |
| `race-tests` | `make bugcheck-race` | `go test -race ./...`. |
| `fuzz-critical` | `make bugcheck-fuzz` | `scripts/fuzz_all.sh`: every fuzz target, one at a time, 60 seconds each by default (see [fuzzing.md](./fuzzing.md)). |
| `determinism` | `make bugcheck-determinism` | `make audit:determinism`: `./tests/determinism/...` and `./tests/consensus/...`. |
| `chaos` | `make bugcheck-chaos` | `make audit:chaos`, which only records the two `pending` checks from `ops/audit/chaos.yaml` via `tools/audit`. It runs no Go test, so `tests/chaos` is not executed by this check (run `go test ./tests/chaos/...`; those tests use the in-process simulation, see [e2e-flows.md](./e2e-flows.md)). |
| `gateway-e2e` | `make bugcheck-gateway` | `go test -run TestEndToEndFinancialFlows ./tests/e2e`. |
| `prod-config` | `scripts/verify_prod_config.sh -c config/prod.toml` | Production configuration safety rails. `config/prod.toml` passes it. |
| `network-hardening` | `make bugcheck-network` | `make audit:netsec`: `./tests/netsec/...`. |
| `performance` | `make bugcheck-perf` | `make audit:perf`: `./tests/perf/...` benchmarks. |
| `protobuf` | `make bugcheck-proto` | `buf lint` and `buf breaking --against ".git#branch=main"`. |
| `docs` | `make bugcheck-docs` | `make audit:docs`, see [docs-quality.md](./docs-quality.md). |

A command that is not installed is recorded with the status `missing`. A check whose command exits successfully is recorded as `passed`; one that exits non-zero is recorded as `failed` (`run_check` in `scripts/bugcheck.sh` reads the exit code in the `else` branch of the `if`). The `chaos` check passes without running any test. `TestEndToEndFinancialFlows` (`gateway-e2e`) and `BenchmarkConsensusFinalityLatency` (`performance`) run against the in-process simulation in `tests/support/cluster`, not the real node, so those checks say nothing about the real node's consensus, lending, swap or governance logic.

## Past runs

| Timestamp (UTC) | Report |
| --- | --- |
<!-- BUGCHECK_HISTORY -->
