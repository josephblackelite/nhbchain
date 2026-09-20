# consensusd Getting Started

`consensusd` (`cmd/consensusd`) runs the node's consensus engine as a gRPC
service. It has no HTTP or JSON-RPC listener. It connects out to a separate
`p2pd` process (`cmd/p2pd`) for peer-to-peer gossip. The all-in-one binary
`cmd/nhb` (used by the validator bootstrap script and `deploy/systemd/nhb.service`)
runs consensus, P2P and JSON-RPC in a single process instead.

## Prerequisites

- Built binaries of `consensusd` and `p2pd` (`go build ./cmd/consensusd`,
  `go build ./cmd/p2pd`).
- A `config.toml` and a validator key source. The key is loaded from, in order:
  `ValidatorKMSEnv` / `ValidatorKMSURI` (an `env:` URI naming an environment
  variable that holds the hex private key), otherwise the keystore file at
  `ValidatorKeystorePath`, decrypted with the passphrase from
  `NHB_VALIDATOR_PASS` (or an interactive prompt when stdin is a terminal;
  `cmd/internal/passphrase`).
- A reachable `p2pd` (default `localhost:9091`).
- Authentication material for the gRPC link: a shared secret, mutual TLS, or
  both. `consensusd` refuses to start when neither is configured
  (`consensus security requires a shared secret or client certificate
  authentication`, `cmd/consensusd/main.go`, `buildConsensusServerSecurity`).

## Security configuration

Both `consensusd` and `p2pd` read the `[network_security]` section of
`config.toml` (`config.NetworkSecurity`, `config/config.go` line 169):

| Key | Meaning |
| --- | --- |
| `SharedSecretEnv` / `SharedSecretFile` / `SharedSecret` | Shared secret, resolved in that order: non-empty environment variable named by `SharedSecretEnv`, then the file, then the inline value. Relative file paths resolve against the config file's directory. |
| `AuthorizationHeader` | gRPC metadata key that carries the secret. Empty means `authorization`; the value is lower-cased. A `Bearer <secret>` value is accepted as well as the bare secret. |
| `ServerTLSCertFile`, `ServerTLSKeyFile` | TLS certificate and key for the `consensusd` gRPC server (both required together). |
| `ClientCAFile`, `AllowedClientCommonNames` | Require and verify client certificates on the server; the allow-list matches certificate DNS names or URIs. Needs the server cert and key. |
| `ServerCAFile`, `ClientTLSCertFile`, `ClientTLSKeyFile`, `ServerName` | Used by `consensusd` when it dials `p2pd`. |
| `AllowInsecure` | Permits plaintext, but only together with the `--allow-insecure` command-line flag and a loopback address; otherwise startup fails. |
| `AllowUnauthenticatedReads` | Read by `p2pd` only. |
| `StreamQueueSize`, `RelayDropLogRatio` | Gossip relay queue size (default 128) and drop-alert ratio (default 0.1). |

The repo `config.toml` sets `SharedSecretEnv = "NHB_NETWORK_SHARED_SECRET"`,
`SharedSecretFile = "/etc/nhb/network.token"`,
`AuthorizationHeader = "x-nhb-network-token"`, TLS file paths under
`/etc/nhb/tls/`, and `AllowInsecure = false`. Those files must exist for
`consensusd` to start with that file.

If the secret resolves to an empty string and no client CA is configured, the
server fails at startup. If TLS material is missing and `AllowInsecure` is not
enabled with `--allow-insecure` on a loopback listener, the server also fails
(`consensus security requires TLS material`).

## Command flags

| Flag | Default | Description |
| --- | --- | --- |
| `--config` | `./config.toml` | Path to the TOML configuration file. |
| `--genesis` | empty | Genesis JSON path. Overrides `NHB_GENESIS` and the config `GenesisFile`. |
| `--allow-autogenesis` | `false` | Development only: create a genesis automatically when none is stored. Overrides `NHB_ALLOW_AUTOGENESIS` and config `AllowAutogenesis`. |
| `--allow-migrate` | `false` | Allow starting with a mismatched state schema (manual migrations only). |
| `--grpc` | `127.0.0.1:9090` | Listen address of the consensus gRPC API. |
| `--p2p` | `localhost:9091` | Address of the `p2pd` gRPC service. |
| `--allow-insecure` | `false` | Development only: permit plaintext gRPC on loopback (also needs `AllowInsecure = true` in config). |
| `--consensus-timeout-proposal` | config value | Wait for a proposal before prevoting. |
| `--consensus-timeout-prevote` | config value | Wait after prevoting. |
| `--consensus-timeout-precommit` | config value | Wait after precommitting. |
| `--consensus-timeout-commit` | config value | Total time allowed to commit before a new round starts. |

Genesis resolution order: `--genesis`, then `NHB_GENESIS`, then config
`GenesisFile`. If `GenesisFile` is set, does not exist, and autogenesis is off,
`consensusd` writes the embedded mainnet genesis (`config.MainnetGenesis`) to
that path.

Environment variables read by `consensusd`:

- `NHB_GENESIS`, `NHB_ALLOW_AUTOGENESIS` as above.
- `NHB_VALIDATOR_PASS`: keystore passphrase.
- The variable named by `SharedSecretEnv` (`NHB_NETWORK_SHARED_SECRET` in the
  repo `config.toml`): the shared secret.
- `NHB_CONSENSUS_TIMEOUT_PROPOSAL`, `NHB_CONSENSUS_TIMEOUT_PREVOTE`,
  `NHB_CONSENSUS_TIMEOUT_PRECOMMIT`, `NHB_CONSENSUS_TIMEOUT_COMMIT`: Go duration
  strings such as `500ms` or `3s`. A flag beats the environment variable, which
  beats the config value. Values must be positive.
- `NHB_ENV`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS`,
  `OTEL_EXPORTER_OTLP_INSECURE`: logging environment and OpenTelemetry export.

## Consensus timeouts

The round timers come from `[consensus]` in `config.toml`:

```toml
[consensus]
ProposalTimeout = "2s"
PrevoteTimeout = "2s"
PrecommitTimeout = "2s"
CommitTimeout = "4s"
MinBlockInterval = "1s"
```

If a key is absent from the file the built-in default applies (2s, 2s, 2s, 4s;
`config.defaultConsensusConfig`). If a key is present it is used as written:
`consensusd` validates the final values with `config.ValidateConsensus` and exits
when any is not positive. The repo `config.toml` currently sets all four to
`"0s"`, so `consensusd` started with that file needs the four flags or
environment variables above (or edited values). The `cmd/nhb` binary passes the
values to `bft.WithTimeouts`, which ignores non-positive durations and keeps the
engine defaults.

## Ports and connectivity

- Consensus gRPC server: `--grpc`, default `127.0.0.1:9090`. Every RPC checks the
  configured authenticators before running.
- `p2pd` link: `--p2p`, default `localhost:9091`. `consensusd` keeps one
  bidirectional gossip stream to `p2pd` open and reconnects with exponential
  backoff starting at 500 ms and capped at 30 s
  (`maintainNetworkStream`). Outbound gossip is held in a queue of up to 4096
  messages (the oldest is dropped when full) and retried with a delay that
  grows from 100 ms to 5 s while the link is down
  (`cmd/consensusd/resilient_broadcaster.go`).

## Services on the gRPC port

`consensusd` registers `consensus.v1.ConsensusService`
(`SubmitTxEnvelope`, `SubmitTransaction`, `GetValidatorSet`, `GetBlockByHeight`,
`GetHeight`, `GetMempool`, `CreateBlock`, `CommitBlock`, `GetLastCommitHash`) and
`consensus.v1.QueryService` (see [Consensus Query API](query-api.md)). Definitions
are in `proto/consensus/v1/`. Server reflection is not registered.

## Health and diagnostics

There is no HTTP health endpoint. Call `GetHeight` on the consensus port; a
successful reply means the service is up. Because reflection is off, `grpcurl`
needs the proto file. The example below assumes TLS; use `-plaintext` instead
of `-cacert` only for a loopback listener started with `--allow-insecure`. Add
`-cert`/`-key` when client certificates are required. The header name must match
`AuthorizationHeader` (shown here with the repo config value):

```bash
grpcurl \
  -import-path proto -proto consensus/v1/consensus.proto \
  -H "x-nhb-network-token: ${NHB_NETWORK_SHARED_SECRET}" \
  -cacert <ca-file-that-signed-the-consensusd-server-certificate> \
  127.0.0.1:9090 consensus.v1.ConsensusService/GetHeight
```

Reconnect notices to `p2pd` are written to stderr
(`Failed to connect to p2pd at ...`, `Network stream terminated: ...`).

## Example startup

```bash
consensusd \
  --config /etc/nhb/validator.toml \
  --p2p p2pd.internal:9091
```

Expose the gRPC port beyond localhost only after configuring TLS and an
authenticator.
