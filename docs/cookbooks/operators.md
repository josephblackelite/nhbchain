# Operator Cookbooks

Reference notes for running the split-service topology: `consensusd` (consensus
and state) plus `p2pd` (the libp2p network daemon it talks to over gRPC). All
flags, environment variables and defaults below are read from the source named
in each section. For the validator onboarding flow see
[docs/validators/onboarding.md](../validators/onboarding.md) and
[docs/ops/validator-runbook.md](../ops/validator-runbook.md).

## Run `consensusd` (`cmd/consensusd/main.go`)

```bash
consensusd --config ./config.toml --grpc 127.0.0.1:9090 --p2p localhost:9091
```

Flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--config` | `./config.toml` | Path to the configuration file. |
| `--genesis` | empty | Genesis block JSON. Overrides `NHB_GENESIS` and the config's `GenesisFile`. |
| `--allow-autogenesis` | `false` | Dev only: create a genesis automatically when none is stored. |
| `--allow-migrate` | `false` | Start even if the stored state schema does not match (manual migrations only). |
| `--grpc` | `127.0.0.1:9090` | Listen address of the consensus gRPC server. |
| `--p2p` | `localhost:9091` | Address of the `p2pd` network gRPC service to connect to. |
| `--allow-insecure` | `false` | Dev only: permit plaintext on loopback. It is required both for the plaintext consensus gRPC listener and for a plaintext connection to `p2pd` (`--p2p`). |
| `--consensus-timeout-proposal`, `-prevote`, `-precommit`, `-commit` | from config | Duration overrides such as `2s`; must be positive. |

Environment variables read by name in `cmd/consensusd/main.go`: `NHB_ENV`,
`NHB_VALIDATOR_PASS` (the validator keystore passphrase), `NHB_GENESIS`,
`NHB_ALLOW_AUTOGENESIS`, `NHB_CONSENSUS_TIMEOUT_PROPOSAL`,
`NHB_CONSENSUS_TIMEOUT_PREVOTE`, `NHB_CONSENSUS_TIMEOUT_PRECOMMIT`,
`NHB_CONSENSUS_TIMEOUT_COMMIT`, and the `OTEL_EXPORTER_OTLP_ENDPOINT`,
`OTEL_EXPORTER_OTLP_HEADERS` and `OTEL_EXPORTER_OTLP_INSECURE` variables for
telemetry. It also reads one further optional API-key variable that it hands to
an external price-oracle adapter registered with the swap oracle aggregator
(line 345); this guide does not cover that adapter.
Variables named by your own configuration are read too: the one named in
`SharedSecretEnv`, and the one in a `ValidatorKMSEnv` / `env:<VARNAME>` key
source.

The validator key comes from the config: `ValidatorKeystorePath` (an encrypted V3
keystore, decrypted with the passphrase from `NHB_VALIDATOR_PASS` or an
interactive prompt), or `ValidatorKMSEnv` / `ValidatorKMSURI` of the form
`env:<VARNAME>` to read the key from an environment variable. With none of these
set the process exits with "validator keystore path not configured".

The gRPC server refuses to start without either a shared secret
(`[network_security]` `SharedSecret`, `SharedSecretFile` or `SharedSecretEnv`) or
a client CA (`ClientCAFile`, with `ServerTLSCertFile` and `ServerTLSKeyFile`).
Without TLS certificates it also refuses to start unless plaintext is enabled.
Plaintext is allowed only with `AllowInsecure = true` in `[network_security]`,
the `--allow-insecure` flag, and a loopback listen address
(`buildConsensusServerSecurity`). The same two settings also gate the plaintext
connection from `consensusd` to `p2pd`, which must additionally be to a loopback
target (`buildNetworkDialOptions`, `cmd/consensusd/main.go:543-620`).

## Run `p2pd` (`cmd/p2pd/main.go`)

```bash
p2pd --config ./config.toml --grpc 127.0.0.1:9091
```

Flags: `--config` (default `./config.toml`), `--genesis`, `--allow-autogenesis`,
`--grpc` (default `127.0.0.1:9091`, the internal network gRPC server that
`consensusd --p2p` connects to) and `--allow-insecure` (dev only: permits the
plaintext gRPC listener on a loopback address; it is honoured only together with
`AllowInsecure = true` in `[network_security]`, `network/security.go:82-88`).

Peer settings are in the `[p2p]` section of the config (`config.P2PSection`):
`Bootnodes`, `PersistentPeers` and `Seeds` are lists of peer addresses, alongside
limits such as `MaxPeers`, `MaxInbound`, `MaxOutbound`, `MinPeers` and
`OutboundPeers`. The P2P listen address is the top-level `ListenAddress` key.

## Deploy with Helm (`deploy/helm`)

The repository ships one chart per service: `consensusd`, `p2pd`, `gateway`,
`governd` and `lendingd`. `consensusd` and `p2pd` are `StatefulSet`s with a
persistent volume (`persistence.size` defaults to `10Gi`); `gateway`, `governd`
and `lendingd` are `Deployment`s. The `consensusd` chart starts the binary with
`--config`, `--grpc :9090` and `--p2p <p2pEndpoint>` (default `p2pd:9091`), and
the `p2pd` chart with `--config` and `--grpc :9091`. The values files set
staging defaults (for example `NetworkName = "nhb-staging"`,
`AllowAutogenesis = true`), so review `values.yaml` before using a chart for
anything else. Kubernetes object names come from the Helm release name.

## Local multi-node example

`examples/compose/mininet/` contains a Docker Compose file and Dockerfiles for
building `consensusd` and `p2pd` from source (`Dockerfile.consensusd`,
`Dockerfile.p2pd`), with configs under `examples/compose/mininet/config/`.
