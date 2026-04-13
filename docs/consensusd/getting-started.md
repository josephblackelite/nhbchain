# consensusd Getting Started

`consensusd` runs the NHB Chain consensus node as a gRPC service. The daemon exposes
consensus-specific APIs without any HTTP or JSON-RPC endpoints, making it suitable
for deployments where the validator stack is isolated from external clients.

## Prerequisites

* Go-built binaries of `consensusd` and `p2pd` in your `$PATH` (see the `cmd/`
  directory for build instructions).
* A populated `config.toml` and keystore compatible with the validator account.
* Access to a running `p2pd` instance reachable over the network.
* Shared-secret or mutual TLS credentials so that only trusted peers can reach
  the consensus gRPC API.

> **Important:** `consensusd` refuses to start when the `[network_security]`
> block is missing or resolves to an empty shared secret. For quick local
> experiments, copy the inline example from `config-local.toml` (set
> `AllowInsecure = true` and provide a short `SharedSecret`). Production
> deployments must supply the token via `SharedSecretEnv` or `SharedSecretFile`,
> leave `AllowInsecure = false`, and provision TLS material so both `consensusd`
> and `p2pd` authenticate each other.

## Command Flags

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--config` | `./config.toml` | Path to the TOML configuration file. |
| `--genesis` | _unset_ | Override path to a genesis JSON file. Takes precedence over the config file and `NHB_GENESIS`. |
| `--allow-autogenesis` | `false` | Development flag enabling automatic genesis creation when no data exists. |
| `--grpc` | `127.0.0.1:9090` | Listen address for the consensus gRPC API. |
| `--p2p` | `localhost:9091` | Target address of the `p2pd` gRPC service. |
| `--consensus-timeout-proposal` | Config (default `2s`) | Wait for a proposal before prevoting. |
| `--consensus-timeout-prevote` | Config (default `2s`) | Wait after prevoting before moving to precommit. |
| `--consensus-timeout-precommit` | Config (default `2s`) | Wait after precommitting before attempting commit. |
| `--consensus-timeout-commit` | Config (default `4s`) | Maximum time allotted for committing a block before starting a new round. |

Environment helpers:

* `NHB_GENESIS` – provides a genesis path when `--genesis` is not supplied.
* `NHB_ALLOW_AUTOGENESIS` – mirrors the `--allow-autogenesis` flag.
* `NHB_VALIDATOR_PASS` – required to decrypt the validator keystore unless KMS is configured.
* `NHB_NETWORK_SHARED_SECRET` (or the value of `network_security.SharedSecretEnv`)
  – supplies the shared-secret token used to authorize gRPC requests. The daemon
  exits during startup if the resolved secret is blank.
* `NHB_CONSENSUS_TIMEOUT_PROPOSAL`, `NHB_CONSENSUS_TIMEOUT_PREVOTE`, `NHB_CONSENSUS_TIMEOUT_PRECOMMIT`, and `NHB_CONSENSUS_TIMEOUT_COMMIT`
  – override the matching CLI flags with duration strings such as `500ms` or `3s`.

## Consensus Timeouts

`consensusd` reads the round timers from the `[consensus]` section of `config.toml`
and falls back to the built-in defaults when the values are omitted. All duration
values accept the Go duration format (`750ms`, `2s`, `1m30s`, etc.).

```toml
[consensus]
ProposalTimeout = "2s"
PrevoteTimeout = "2s"
PrecommitTimeout = "2s"
CommitTimeout = "4s"
```

Operators can adjust the timers at runtime with CLI flags or the environment
variables listed above to better match their network latency profile.

## Ports and Connectivity

* Consensus gRPC service: defaults to `127.0.0.1:9090` and refuses
  unauthenticated connections.
* P2P backhaul (p2pd gRPC): defaults to `localhost:9091` and is maintained with
  exponential backoff and backlog replay on reconnect.

The daemon keeps the consensus ↔︎ P2P bidirectional stream alive, automatically
re-dialling `p2pd` and re-flushing queued gossip after transient failures.

## Health and Diagnostics

`consensusd` does not expose an HTTP health check. Operators can rely on the
following gRPC level checks:

* Establish a gRPC connection to the consensus port and invoke `GetHeight`
  (defined in `consensus.v1.ConsensusService`). A successful response confirms
  the service is healthy.
* Inspect logs for reconnect notices emitted when the P2P link drops.

For liveness probes in container environments, use a lightweight gRPC probe such
as [`grpcurl`](https://github.com/fullstorydev/grpcurl) and include the shared
secret or present a client certificate:

```bash
grpcurl \
  -plaintext \
  -H 'authorization: Bearer ${NHB_NETWORK_SHARED_SECRET}' \
  localhost:9090 consensus.v1.ConsensusService/GetHeight
```

## Example Startup

```bash
consensusd \
  --config /etc/nhb/validator.toml \
  --p2p p2pd.internal:9091
```

The gRPC server enforces the shared secret (and mutual TLS when configured)
before executing any RPC, so expose the port outside of localhost only after
provisioning the required credentials.
