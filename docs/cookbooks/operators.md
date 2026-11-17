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

The genesis file is chosen in this order (`resolveGenesisPath`): `--genesis`, then
`NHB_GENESIS`, then the config's `GenesisFile`. If the config names a file that
does not exist and autogenesis is off, `consensusd` writes the genesis embedded in
the binary (`config.MainnetGenesis`, `config/genesis.relaunch.json`, the live
network's genesis) to that path and uses it. With no path at all it exits unless
autogenesis is enabled.

Environment variables read by name in `cmd/consensusd/main.go`: `NHB_ENV`,
`NHB_VALIDATOR_PASS` (the validator keystore passphrase), `NHB_GENESIS`,
`NHB_ALLOW_AUTOGENESIS`, `NHB_CONSENSUS_TIMEOUT_PROPOSAL`,
`NHB_CONSENSUS_TIMEOUT_PREVOTE`, `NHB_CONSENSUS_TIMEOUT_PRECOMMIT`,
`NHB_CONSENSUS_TIMEOUT_COMMIT`, and the `OTEL_EXPORTER_OTLP_ENDPOINT`,
`OTEL_EXPORTER_OTLP_HEADERS` and `OTEL_EXPORTER_OTLP_INSECURE` variables for
telemetry, and `NHB_METRICS_ADDR`: when it is set to a listen address (for example
`127.0.0.1:9101`), `consensusd` serves Prometheus metrics at `/metrics` there. The
endpoint is unauthenticated, so bind it to a loopback or private address; a
failure to start it is logged and never stops the validator
(`observability/metrics_server.go`).
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
target (`buildNetworkDialOptions` in `cmd/consensusd/main.go`).

### Block pace and validator files

The `[consensus]` section of the config holds the round timeouts
(`ProposalTimeout`, `PrevoteTimeout`, `PrecommitTimeout`, `CommitTimeout`) and
`MinBlockInterval`. When the key is absent the timeouts default to 2s, 2s, 2s and
4s and `MinBlockInterval` defaults to 1s (`defaultConsensusConfig` in
`config/config.go`). `consensusd` exits at startup if a timeout is not positive
or if `MinBlockInterval` is negative or above half the commit timeout
(`ConsensusProblems` in `config/validate.go`, called from `cmd/consensusd/main.go`
after the flag and environment overrides are applied). `MinBlockInterval` is the
least time this validator waits, after it sees a block commit, before it starts
the next height; `0s` turns the wait off. The engine itself also lowers a value
above half the commit timeout to that (`bft.WithMinBlockInterval`). It is local to the validator, not a rule blocks are
checked against. Every quantity counted in blocks (epoch length, emission per
epoch, lending interest per block) follows the pace it sets; see
[block cadence](../consensus/block-cadence.md). `consensusd` logs the interval in
force at startup ("consensus: minimum block interval").

`consensusd` writes two files in the configured `DataDir` that must stay with the
validator's keys and chain data: `polc_lock.json` (the engine's lock snapshot) and
`bft_sign_state.json` (the votes this validator has signed, so a restart cannot
make it sign a different vote for a round it already voted in)
(`cmd/consensusd/main.go`; see the [validator runbook](../ops/validator-runbook.md)).

## Run `p2pd` (`cmd/p2pd/main.go`)

```bash
p2pd --config ./config.toml --grpc 127.0.0.1:9091
```

Flags: `--config` (default `./config.toml`), `--genesis`, `--allow-autogenesis`,
`--grpc` (default `127.0.0.1:9091`, the internal network gRPC server that
`consensusd --p2p` connects to) and `--allow-insecure` (dev only: permits the
plaintext gRPC listener on a loopback address; it is honoured only together with
`AllowInsecure = true` in `[network_security]`, and only when no TLS material is
configured: `BuildServerSecurity` in `network/security.go`).

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

Do not use a fresh node started from genesis to join the live network: block sync
from genesis is not supported on it. New validators join from a verified state
snapshot; see [snapshot onboarding](../validators/snapshot-onboarding.md).
