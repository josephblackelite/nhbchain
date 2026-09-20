# Local deployment with Docker Compose

`deploy/compose/docker-compose.yml` builds several NHB services from source and
runs them with Docker Compose. It is a development stack, not a production
deployment. Every service uses `network_mode: host`, which Docker Desktop does
not support (the compose file says to override `network_mode` and the bind
addresses in that case). All configured listen addresses are on `127.0.0.1`.

## Commands

From the repository root (`Makefile`):

```sh
make up     # docker compose -f deploy/compose/docker-compose.yml up -d --build
make down   # docker compose -f deploy/compose/docker-compose.yml down -v
```

`make down` passes `-v`, so it also deletes the named volumes. Remove `-v` from
the `down` target in the `Makefile` to keep state.

## Services

Each image is built from `deploy/compose/Dockerfile` (build argument `TARGET`
selects the Go package; `GO_VERSION` defaults to `1.23.0`). The runtime image is
`debian:bookworm-slim` running as `nobody`.

| Service | Package | Listen address (from the compose file or its config) |
| ------- | ------- | -------------------------------------------------- |
| `p2pd` | `./cmd/p2pd` | `127.0.0.1:26656` (p2p, `config/p2p.toml`), `127.0.0.1:9091` (gRPC, `--grpc`) |
| `consensusd` | `./cmd/consensusd` | `127.0.0.1:9090` (gRPC, `--grpc`) |
| `lendingd` | `./services/lendingd` | `127.0.0.1:50053` (gRPC, `config/lendingd.yaml`) |
| `identity-gateway` | `./services/identity-gateway/cmd/identity-gateway` | `127.0.0.1:8095` (`IDENTITY_GATEWAY_LISTEN`) |
| `governd` | `./services/governd` | `127.0.0.1:50061` (gRPC, `config/governd.yaml`) |
| `gateway` | `./cmd/gateway` | `127.0.0.1:8080` (`config/gateway.yaml`) |

There is no service in this stack, or anywhere in this repository, for the swap
backend that the gateway requires.

`consensusd` reads `RPCAddress` (`127.0.0.1:8081`) and `ListenAddress`
(`127.0.0.1:6002`) from `config/consensus.toml`, but `cmd/consensusd` does not
open either; it only serves gRPC. The JSON-RPC HTTP server is part of `cmd/nhb`.

Startup order (`depends_on`): `consensusd` waits for `p2pd` to pass its
healthcheck (`nc -z localhost 9091`); `governd` waits for `consensusd` to start;
`gateway` waits for `lendingd`, `governd` and `consensusd`.

State: named volumes `p2p-data`, `consensus-data` and `identity-gateway-data`
(mounted at `/var/lib/nhb`). `lendingd`, `governd` and `gateway` have no volume.

`p2pd` and `consensusd` run with `--allow-autogenesis` and
`NHB_ALLOW_AUTOGENESIS=true`, and the shipped TOML sets `AllowAutogenesis = true`
and `GenesisFile = ""`, so no genesis file is required.

## Required inputs

The compose file refuses to start without these:

- `deploy/compose/.env` must exist: `consensusd` and `identity-gateway` use
  `env_file: .env`. The repository does not ship an example. From the code,
  `consensusd` needs `NHB_VALIDATOR_PASS` (keystore passphrase) and
  `identity-gateway` needs `IDENTITY_EMAIL_SALT`, `IDENTITY_GATEWAY_API_KEYS`
  and `IDENTITY_GATEWAY_NODE_URL` (it exits with `<name> is required`
  otherwise).
- `GOVERND_SIGNER_KEY` (hex signer key) and `GOVERND_TLS_KEY_FILE` (path to the
  TLS private key) must be set when running compose. `GOVERND_TLS_CERT_FILE` has
  a compose default, `./services/governd/config/server.crt`, but that path is
  resolved relative to `deploy/compose` and no such file exists in this
  repository, so set `GOVERND_TLS_CERT_FILE` to a real certificate file as well
  (`deploy/compose/docker-compose.yml`).

## What the shipped configuration does not satisfy

Reading the service code against the shipped files, these services will exit at
startup unless the configuration is extended. This has not been run; it is
derived from the code paths named.

- `consensusd`: with no `[network_security]` section and no `--allow-insecure`,
  `buildNetworkDialOptions` returns `network security configuration is missing
  TLS material` (`cmd/consensusd/main.go`). Its gRPC server also requires a
  shared secret or client-certificate authentication
  (`buildConsensusServerSecurity`). `p2pd` runs the same kind of network
  security initialisation (`cmd/p2pd/main.go`).
- `gateway`: the compose service sets `NHB_ENV=local`. Unless `NHB_ENV=dev`, the
  gateway requires `https://` for every backend endpoint and a TLS certificate
  and key, and `config/gateway.yaml` has `http://` endpoints and no TLS files
  (see [Gateway Overview](../gateway/overview.md#transport-security)). It also
  requires an endpoint for the swap backend, which defaults to
  `http://127.0.0.1:7102`.
- `governd`: `config/governd.yaml` sets `consensus_client.tls.cert_env` to
  `GOVERND_CLIENT_CERT_PATH` and `key_env` to `GOVERND_CLIENT_KEY_PATH`, and the
  compose file sets neither variable, so `consensus_client.tls.cert_env
  GOVERND_CLIENT_CERT_PATH is empty` is returned
  (`services/governd/config/config.go`, `ClientConfig.Validate`). The same file
  has `allow_insecure: false` and an empty shared-secret `token`, so removing
  the TLS settings would hit `requires tls material or shared-secret
  authentication unless allow_insecure=true`.
- `lendingd`: `config/lendingd.yaml` only sets `listen`.
  `services/lendingd/config/config.go` requires a TLS certificate and key unless
  `tls.allow_insecure: true`, and at least one `auth.api_tokens` entry or
  `auth.mtls.allowed_common_names` entry.

## Customisation

- Configuration files live in `deploy/compose/config` and are mounted read-only
  into the containers.
- Environment variables can be added under `environment:` in the compose file.
- To change the build (Go version, extra tooling), edit
  `deploy/compose/Dockerfile`. `go.mod` declares `go 1.24.0` with
  `toolchain go1.24.3`, while the Dockerfile's default `GO_VERSION` is `1.23.0`.

## Troubleshooting

Use `docker compose -f deploy/compose/docker-compose.yml logs <service>` to read
the startup error of a service that exits. The messages quoted above are the
ones the code prints.
