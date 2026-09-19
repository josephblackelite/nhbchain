# Audit Readiness Guide

This guide lists what an independent assessor can find in this repository and how to reproduce it.

## Scope

The code most relevant to an audit lives in `consensus/` (BFT engine and POTSO), `core/` (state transition, staking, sponsorship), `native/` (escrow, lending, swap and other native modules), `rpc/` (JSON-RPC, WebSocket and gRPC server), `p2p/` (peer networking and handshake), `config/` (configuration loading and defaults), `gateway/` and `services/` (HTTP services) and `cmd/` (binaries).

## Artifacts in the repository

- **Revision under audit.** No commit hash is recorded in the repository. Record the exact commit (`git rev-parse HEAD`) with the audit.
- **Build.** CI (`.github/workflows/ci.yml`) builds the node with Go 1.24.3 using `go build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/nhb ./cmd/nhb`; `go.mod` declares `go 1.24.0` and `toolchain go1.24.3`. `go.sum` is committed.
- **Configuration samples.** `config.toml`, `config/prod.toml`, `config/genesis*.json`, `config/security.yaml`, `gateway/config.yaml`, and the deployment manifests under `deploy/` (`compose/`, `helm/`, `systemd/`, `env/`).
- **Tests and fixtures.** Go tests throughout the tree, integration tests under `tests/`, golden files under `tests/golden/`, and audit phase plans and fixtures under `ops/audit/`.
- **Audit tooling.** See [../audit/overview.md](../audit/overview.md) for the `make audit:*` and `make bugcheck` targets.
- **Change history.** `docs/CHANGELOG.md` and `docs/changelogs/` record documentation changes.
- **Related security documents.** [transport.md](./transport.md), [network-hardening.md](./network-hardening.md), [api-auth.md](./api-auth.md), [handshake.md](./handshake.md), [snapshots-audit.md](./snapshots-audit.md), [mint-controls.md](./mint-controls.md).

## Embargo and disclosure

Findings from an audit are handled under the same timelines as vulnerability reports; see [disclosure.md](./disclosure.md).

## Contacts and encryption

- **Program lead:** `audit@nhbcoin.com`
- **Security team:** `security@nhbcoin.com`
- **PGP key:** [`repository-pgp-key.asc`](./repository-pgp-key.asc) is an RSA 4096 key for `NHBCoin Security Team <security@nhbcoin.com>` with an encryption subkey. Its fingerprint is `8C12 7674 689A AB92 A4DE  4643 E847 50CA 0E2F 4459`. Confirm that the fingerprint of the key you import matches before encrypting anything to it.
