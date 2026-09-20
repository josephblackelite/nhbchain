# Service Directory

The binaries and services in this repository, with the configuration surface
each one reads in code. Anything not listed here is not implemented in this
repository.

## Gateway (`cmd/gateway`)

- **Protocol:** HTTP(S) reverse proxy in front of `lendingd`, `governd`,
  `consensusd` and a swap backend. The lending routes call `lendingd` over gRPC; other routes are
  proxied as HTTP.
- **Responsibilities:** JWT bearer authentication with per-route scopes, per-route
  rate limits, CORS, Prometheus metrics, OpenTelemetry tracing, and the optional
  `/rpc` JSON-RPC compatibility dispatcher.
- **Configuration:** `--config <yaml>`, `--compat-mode`, `--allow-insecure`, and
  the environment variables `NHB_ENV`, `NHB_COMPAT_MODE`, `NHB_GATEWAY_AUTO_HTTPS`,
  `NHB_GATEWAY_LENDING_URL`, `NHB_GATEWAY_SWAP_URL`, `NHB_GATEWAY_GOV_URL` and
  `NHB_GATEWAY_CONSENSUS_URL`. Rate limits are the `rateLimits` list in the YAML
  file. Details: [Gateway Overview](../gateway/overview.md).
- **Not in this repository:** the swap backend. The gateway still requires an
  endpoint for it (default `http://127.0.0.1:7102`) and routes `/v1/swap` to it.

## Node (`cmd/nhb`)

- **Protocol:** the node the `nhb.service` unit runs (see
  [One-shot deployment](../deploy/one-shot-deploy.md)). It runs consensus, the
  peer-to-peer layer and the JSON-RPC HTTP server (`RPCAddress`) in one process.
- **Flags:** `-config` (default `./config.toml`), `-genesis` (overrides
  `NHB_GENESIS` and the config's `GenesisFile`), `-allow-autogenesis`,
  `-allow-migrate`.
- **Environment:** `NHB_VALIDATOR_PASS` (keystore passphrase), `NHB_GENESIS`,
  `NHB_ALLOW_AUTOGENESIS`, `NHB_ENV`, `NHB_METRICS_ADDR` (optional Prometheus
  listener, see [Observability](../ops/observability.md)), `OTEL_EXPORTER_OTLP_*`,
  and the block-production tuning variables in the
  [Validator Operations Runbook](../ops/validator-runbook.md#operator-levers).
- At start it logs a `configuration problem` warning for every problem in the
  configuration and does not stop (see
  [Runtime Configuration Guardrails](../ops/configuration.md)).

## Consensus daemon (`cmd/consensusd`)

- **Protocol:** gRPC (`ConsensusService` and `QueryService`), default
  `127.0.0.1:9090`. It does not serve JSON-RPC over HTTP; that server is in
  `cmd/nhb`.
- **Flags:** `-config` (default `./config.toml`), `-genesis` (overrides
  `NHB_GENESIS` and the config's `GenesisFile`), `-grpc` (default
  `127.0.0.1:9090`), `-p2p` (address of the `p2pd` gRPC service, default
  `localhost:9091`), `-allow-autogenesis`, `-allow-migrate`, `-allow-insecure`,
  and `-consensus-timeout-proposal|prevote|precommit|commit`.
- **Environment:** `NHB_VALIDATOR_PASS` (keystore passphrase), `NHB_GENESIS`,
  `NHB_ALLOW_AUTOGENESIS`, `NHB_CONSENSUS_TIMEOUT_PROPOSAL`,
  `NHB_CONSENSUS_TIMEOUT_PREVOTE`, `NHB_CONSENSUS_TIMEOUT_PRECOMMIT`,
  `NHB_CONSENSUS_TIMEOUT_COMMIT`, `NHB_ENV`, `NHB_METRICS_ADDR`,
  `OTEL_EXPORTER_OTLP_*`.
- **Requirements:** a `[network_security]` section giving TLS material (or
  `AllowInsecure = true` together with `-allow-insecure` on a loopback target),
  and a shared secret or client-certificate authentication for its own gRPC
  server (`cmd/consensusd/main.go`).
- See [Runtime Configuration Guardrails](../ops/configuration.md) for the
  `config.toml` checks.

## P2P daemon (`cmd/p2pd`)

- **Protocol:** an internal gRPC network service (`-grpc`, default
  `127.0.0.1:9091`) that `consensusd` connects to, plus the p2p listener
  configured by `ListenAddress` in the config file.
- **Flags:** `-config` (default `./config.toml`), `-genesis`,
  `-allow-autogenesis`, `-grpc`, `-allow-insecure`.

## Lending service (`services/lendingd`, `services/lending`)

Two entry points register the same gRPC `LendingService`
(`services/lending/server`), which forwards to a node's JSON-RPC:

- `services/lendingd` (used by the compose stack, the Helm chart and CI image
  builds): `--config <yaml>` (default `services/lending/config.yaml`). Keys:
  `listen` (default `:50053`), `node_rpc_url` (default `https://127.0.0.1:8081`),
  `node_rpc_token`, `shared_secret_header` (default `X-NHB-Shared-Secret`),
  `shared_secret_value`, `rate_limit_per_min` (default `120`), `tls.cert`,
  `tls.key`, `tls.client_ca`, `tls.allow_insecure`, `auth.api_tokens`,
  `auth.mtls.allowed_common_names`. TLS material is required unless
  `tls.allow_insecure` is true, and at least one API token or mTLS common name
  is required (`services/lendingd/config/config.go`).
- `services/lending` (standalone binary): environment variables `LEND_NODE_RPC_URL`
  (default `https://127.0.0.1:8081`), `LEND_NODE_RPC_TOKEN`,
  `LEND_SHARED_SECRET_HEADER`, `LEND_SHARED_SECRET`, `LEND_TLS_CERT_FILE`,
  `LEND_TLS_KEY_FILE`, `LEND_TLS_CLIENT_CA_FILE`, `LEND_ALLOW_INSECURE`,
  `LEND_LISTEN` (default `0.0.0.0:9444`), `LEND_RATE_PER_MIN` (default `120`),
  `LEND_MTLS_REQUIRED`, `LEND_ALLOWED_CNS`; each also has a command-line flag
  (`-listen`, `-tls-cert`, `-mtls-required`, `-mtls-allowed-cn`, ...). It
  also starts an HTTP `/healthz` listener on a random loopback port.

The transport is gRPC in both cases (not REST).

## Governance service (`services/governd`)

- **Protocol:** gRPC, `gov.v1.Msg` and `gov.v1.Query` (including `SetPauses`).
- **Configuration:** `--config <yaml>` (default `services/governd/config.yaml`).
  Keys include `listen`, `consensus`, `chain_id`, `signer_key` /
  `signer_key_file` / `signer_key_env`, `nonce_start`, `nonce_store_path`, `fee`,
  `tls`, `auth` and `consensus_client` (`services/governd/config/config.go`).
  The compose file supplies the signer key through `GOVERND_SIGNER_KEY`.

## Snapshot tool (`cmd/nhb-snapshot`)

Standalone tool used by `scripts/make-snapshot.sh` and `scripts/deployvalidator.sh`.
Commands: `info`, `refs`, `pack`, `verify`, `extract`, `manifest`, `check-config`,
`wait-synced` and `version` (`cmd/nhb-snapshot/main.go`). See
[Snapshot operations](../ops/snapshots.md) and
[Onboarding a validator from a snapshot](../validators/snapshot-onboarding.md).

## Other services

- `services/gov-keeper`: a standalone daemon that polls `gov_list` on a
  validator's JSON-RPC and submits the Finalize/Queue/Execute transactions that
  proposals become eligible for (flags `-rpc`, `-jwt-secret-env`, `-jwt-issuer`,
  `-jwt-audience`, `-key`, `-poll-interval-seconds`, `-list-limit`).
- `services/identity-gateway`: HTTP identity service. Environment:
  `IDENTITY_GATEWAY_LISTEN` (default `:8095`), `IDENTITY_GATEWAY_PORT`,
  `IDENTITY_GATEWAY_DB` (default `identity-gateway.db`), and the required
  `IDENTITY_EMAIL_SALT`, `IDENTITY_GATEWAY_API_KEYS`, `IDENTITY_GATEWAY_NODE_URL`.
  See [identity-gateway](../identity/identity-gateway.md).
- `services/escrow-gateway`: REST gateway for escrow and P2P trade flows. See
  [Escrow gateway](../escrow/nhbchain-escrow-gateway.md) and
  [Escrow gateway webhook queue operations](../ops/webhooks.md).

Refer to the [migration guide](../migrate/services.md) when moving from the
JSON-RPC node to the gateway topology.

---

Additional module references:

- [Escrow Service API](./escrow.md)
