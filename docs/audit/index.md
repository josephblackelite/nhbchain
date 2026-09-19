# Bugcheck history

[`latest.md`](./latest.md) holds the most recent report copied in by `scripts/publish_bugcheck.sh`. In CI, the `bugcheck-linux` job in `.github/workflows/ci.yml` runs `scripts/bugcheck.sh` and then `scripts/publish_bugcheck.sh`, and uploads the `docs/audit/` files as a workflow artifact. The publish step does not commit anything. It copies `artifacts/bugcheck-<timestamp>.json` into `history/` only if that file exists, and the bugcheck script currently never creates it (see below). The `bugcheck-linux` job also runs `scripts/verify_prod_config.sh -c config/prod.toml` as a separate step before the bugcheck, and that step currently fails on `config/prod.toml`.

## What the bugcheck runs

`make bugcheck` (`scripts/bugcheck.sh`) runs these checks in order, each marked `critical`. Every check writes `logs/<id>.log`, and the run writes `audit/bugcheck-<timestamp>.md`. The script is written to also write `artifacts/bugcheck-<timestamp>/summary.json` and copy it to `artifacts/bugcheck-<timestamp>.json`, but it currently never reaches that step (see the caveats below).

| Check id | Command | Covers |
| --- | --- | --- |
| `bugcheck-tools` | `make bugcheck-tools` | Installs `golangci-lint`, `staticcheck`, `gosec`, `govulncheck` and `buf` when missing. |
| `static-security` | `make bugcheck-static` | `golangci-lint`, `go vet`, `staticcheck`, `gosec`, `govulncheck`. |
| `race-tests` | `make bugcheck-race` | `go test -race ./...`. |
| `fuzz-critical` | `make bugcheck-fuzz` | `go test -run ^$ -fuzz=Fuzz -fuzztime=60s ./tests/... ./p2p` (see [fuzzing.md](./fuzzing.md)). |
| `determinism` | `make bugcheck-determinism` | `make audit:determinism`: `./tests/determinism/...` and `./tests/consensus/...`. |
| `chaos` | `make bugcheck-chaos` | `make audit:chaos`, which only records the two `pending` checks from `ops/audit/chaos.yaml` via `tools/audit`. It runs no Go test, so `tests/chaos` is not executed by this check (run `go test ./tests/chaos/...`; those tests use the in-process simulation, see [e2e-flows.md](./e2e-flows.md)). |
| `gateway-e2e` | `make bugcheck-gateway` | `go test -run TestEndToEndFinancialFlows ./tests/e2e`. |
| `prod-config` | `scripts/verify_prod_config.sh -c config/prod.toml` | Production configuration safety rails. Currently `config/prod.toml` fails this script (`ListenAddress = ":6001"` and `RPCAddress = ":8080"` bind to all interfaces, and `global.Fees.owner_wallet` and the `NHB` and `ZNHB` fee-asset `owner_wallet` values are unset). |
| `network-hardening` | `make bugcheck-network` | `make audit:netsec`: `./tests/netsec/...`. |
| `performance` | `make bugcheck-perf` | `make audit:perf`: `./tests/perf/...` benchmarks. |
| `protobuf` | `make bugcheck-proto` | `buf lint` and `buf breaking --against ".git#branch=main"`. |
| `docs` | `make bugcheck-docs` | `make audit:docs`, see [docs-quality.md](./docs-quality.md). Currently fails because `ops/audit-pack/BUILD_STEPS.md` does not exist. |

A command that is not installed is recorded with the status `missing`. A check whose command exits successfully is recorded as `passed`.

Known defects in `scripts/bugcheck.sh` (as of this commit):

- A check whose command fails is also recorded as `passed`. In `run_check` (lines 83-90), `exit_code=$?` runs inside `if ! (...); then`, where `$?` is the result of the negated test and is therefore 0, so the `failed` branch is never taken. Only `missing` commands set the overall status to `failed`. Read `logs/<id>.log` for each check instead of trusting the table.
- The JSON summary block (line 141) uses `local first=1` outside a function. Under `set -e` bash prints `local: can only be used in a function` and the script exits with status 1 there, so `summary.json`, the copy in `artifacts/` and the final "GREEN LIGHT" or failure line are never produced.
- `make bugcheck-chaos` does not run `tests/chaos`, `prod-config` fails on `config/prod.toml`, and `docs` fails on a missing hashed file (see the table).

## Past runs

| Timestamp (UTC) | Report |
| --- | --- |
<!-- BUGCHECK_HISTORY -->
