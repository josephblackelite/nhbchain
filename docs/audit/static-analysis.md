# Static Analysis Guide

This page lists the static-analysis tooling that the repository wires up and how to run it. Every command below exists in the `Makefile` or in `.github/workflows/`.

## Toolchain

| Tool | How it is invoked | Configuration |
| --- | --- | --- |
| `golangci-lint` | `make audit:static`, `make bugcheck-static` | `.golangci.yml`: `govet`, `errcheck`, `staticcheck`, `ineffassign`, `gosec`, `revive`, `misspell`, `unparam`, `gocyclo` (min complexity 15, not applied to `_test.go`), `prealloc`; 5 minute timeout |
| `go vet` | `make bugcheck-static` | Go defaults |
| `staticcheck` | `make audit:static`, `make bugcheck-static` | defaults |
| `gosec` | `make bugcheck-static` | defaults |
| `govulncheck` | `make audit:static`, `make bugcheck-static` | defaults |
| `buf lint`, `buf breaking` | `make audit:static`, `make bugcheck-proto` | `buf.work.yaml` (directory `proto`) and `proto/buf.yaml` (lint `DEFAULT`, breaking `FILE`); `buf breaking` compares against `.git#branch=main` |
| English audit | `make audit:english` | see [README.md](./README.md) |

There is no `make lint` or `make deps` target, no root `lint` npm script and no Semgrep configuration in this repository.

## Running the tools

1. **Install the tools.** `make bugcheck-tools` runs `go install ...@latest` for `golangci-lint`, `staticcheck`, `gosec`, `govulncheck` and `buf` when they are not already on `PATH`.
2. **Run the Go and protobuf checks.** Either target below is a chain of the commands in the table; each stops at the first command that fails.

   ```bash
   make audit:static     # go mod tidy, golangci-lint, govulncheck, staticcheck, buf lint, buf breaking; logs in logs/
   make bugcheck-static  # golangci-lint, go vet, staticcheck, gosec, govulncheck
   ```

   `make audit:static` tees each tool's output to `logs/go-mod-tidy.log`, `logs/golangci-lint.log`, `logs/govulncheck.log`, `logs/staticcheck.log`, `logs/buf-lint.log` and `logs/buf-breaking.log`.
3. **Run the English audit** (optional): `make audit:english`.

The CI workflow (`.github/workflows/ci.yml`) additionally runs a `git-secrets` scan for PEM private-key headers (`scripts/check_private_keys.sh`) before building and testing, and its `bugcheck-linux` job runs `scripts/bugcheck.sh`, which includes the static, protobuf and documentation checks (see [index.md](./index.md)). `.github/workflows/deploy.yml` also runs `buf lint` and `buf breaking`.

## Triage workflow

- Classify each finding by impact and exploitability.
- Confirm whether the flagged code is reachable in a production configuration; record false positives with the reason.
- Track each accepted finding with an owner and a fix version.
- Re-run the tools after a fix to confirm the finding no longer appears.
- Record any lint suppression together with the rationale.

## Reporting

- Keep the logs the commands write (`logs/*.log`) with the audit record.
- Summarise outstanding issues, including compensating controls, in the final audit report.
