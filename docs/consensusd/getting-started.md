# consensusd Getting Started

`consensusd` runs the NHB Chain consensus node as a gRPC service. The daemon exposes
consensus-specific APIs without any HTTP or JSON-RPC endpoints, making it suitable
for deployments where the validator stack is isolated from external clients.

## Prerequisites

* Go-built binaries of `consensusd` and `p2pd` in your `$PATH` (see the `cmd/`
  directory for build instructions).
* A populated `config.toml` and keystore compatible with the validator account.
* Access to a running `p2pd` instance reachable over the network.

## Command Flags

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--config` | `./config.toml` | Path to the TOML configuration file. |
| `--genesis` | _unset_ | Override path to a genesis JSON file. Takes precedence over the config file and `NHB_GENESIS`. |
| `--allow-autogenesis` | `false` | Development flag enabling automatic genesis creation when no data exists. |
| `--grpc` | `:9090` | Listen address for the consensus gRPC API. |
| `--p2p` | `localhost:9091` | Target address of the `p2pd` gRPC service. |

Environment helpers:

* `NHB_GENESIS` – provides a genesis path when `--genesis` is not supplied.
* `NHB_ALLOW_AUTOGENESIS` – mirrors the `--allow-autogenesis` flag.
* `NHB_VALIDATOR_PASS` – required to decrypt the validator keystore unless KMS is configured.

## Ports and Connectivity

* Consensus gRPC service: defaults to `:9090`.
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
as [`grpcurl`](https://github.com/fullstorydev/grpcurl):

```bash
grpcurl -plaintext localhost:9090 consensus.v1.ConsensusService/GetHeight
```

## Example Startup

```bash
consensusd \
  --config /etc/nhb/validator.toml \
  --grpc 0.0.0.0:9090 \
  --p2p p2pd.internal:9091
```

Run `consensusd` alongside `p2pd` (see `/examples/compose/mininet`) to provide the
full networking stack.
