# Supply Chain Security Guide

This page records what the repository does today for dependency and build integrity, and how to check it.

## Dependencies

- **Lockfiles.** `go.sum` and `package-lock.json` are committed; `go.work` joins the root module and `sdk/`.
- **Toolchain.** `go.mod` declares `go 1.24.0` and `toolchain go1.24.3`; CI uses Go 1.24.3.
- **Vulnerability scanning.** `govulncheck ./...` runs in `make bugcheck-static` and `make audit:static`, and optionally in the English audit (`make audit:english`, see [../audit/README.md](../audit/README.md)). `go list -m -u all` lists available module updates.
- **Static checks.** `gosec`, `staticcheck` and `golangci-lint` run in the same targets (see [../audit/static-analysis.md](../audit/static-analysis.md)).

## Build pipeline

- CI (`.github/workflows/ci.yml`) runs `go mod tidy`, then builds the node with `go build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/nhb ./cmd/nhb` and runs `go test ./...` on Ubuntu, macOS and Windows.
- A `secrets-scan` job runs `scripts/check_private_keys.sh`, which uses `git-secrets` to fail on PEM private-key headers in tracked or staged files. The other CI jobs wait for it.
- `.github/workflows/deploy.yml` (pushes to a branch named `master`, `v*` tags, manual dispatch; the upstream default branch is `main`, so a push to `main` does not match) runs `buf lint` and `buf breaking`, then builds container images for `gateway`, `consensusd`, `p2pd`, `lendingd` and `governd` from `deploy/compose/Dockerfile` and pushes them to GitHub Container Registry with tags for the git tag, the commit SHA and, only on `refs/heads/master`, `latest`, then packages and pushes the Helm charts under `deploy/helm/`.
- The repository contains no artifact-signing (for example `cosign`) or SBOM-generation configuration.

## Checking a build

- Build with the same Go version and flags as CI and compare hashes of the resulting binaries between machines.
- Run `go mod verify` to check downloaded modules against `go.sum`.

## Incident handling

If a dependency or build step is suspected to be compromised:

1. Stop releases and notify stakeholders.
2. Identify the affected module or step.
3. Pin or replace the dependency, regenerate artifacts and record the change.
4. Follow [release-process.md](./release-process.md) for disclosure and rollout.
