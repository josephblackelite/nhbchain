# Local deployment with Docker Compose

The repository includes a development stack under `deploy/compose` that
builds all NHB services from source and runs them with Docker Compose.
It is intended for one-shot local integration testing and API exploration.
All services bind to `127.0.0.1` and the compose file opts into Docker's host
network so nothing is reachable from other hosts unless you deliberately
override the bind addresses. Host networking is only available on Linux; use a
compose override or custom deployment when developing on macOS/Windows.
Adjust the configuration files or compose overrides before exposing ports
beyond loopback.

## Prerequisites

- Docker Engine 24+
- Docker Compose plugin (or Docker Desktop)
- Make

## Bring the stack up

From the repository root run:

```sh
make up
```

The target builds fresh images for:

- `p2pd`
- `consensusd`
- `lendingd` (preview – disabled by default in Helm)
- `swapd`
- `governd`
- `gateway`

Compose mounts configuration files from `deploy/compose/config`. All stateful
services write data to named Docker volumes so containers can be restarted
without losing state.

### Services & ports

| Service      | Port | Notes |
|--------------|------|-------|
| gateway      | 127.0.0.1:8080 | REST gateway |
| swapd        | 127.0.0.1:7074 | HTTP oracle |
| governd      | 127.0.0.1:50061 | gRPC |
| lendingd     | 127.0.0.1:50053 | gRPC (preview – returns UNIMPLEMENTED) |
| consensusd   | 127.0.0.1:9090 | gRPC (public) |
| consensusd   | 127.0.0.1:8081 | HTTP RPC |
| p2pd         | 127.0.0.1:26656 | Tendermint-style P2P |
| p2pd         | 127.0.0.1:9091 | internal gRPC |

`consensusd` and `p2pd` are configured with `AllowAutogenesis=true`, so the
stack will mint a throwaway genesis if none exists.

### Shut the stack down

```sh
make down
```

This stops and removes all containers plus the named volumes created by the
stack. To preserve state, remove the `-v` flag from the `docker compose down`
command in the Makefile before stopping the services.

### Customisation tips

- All configuration files live in `deploy/compose/config`. Copy them locally
  and point the Compose services at your versions to experiment with
  different parameters.
- Set additional environment variables for telemetry or debugging by editing
  `deploy/compose/docker-compose.yml`.
- The build context uses `deploy/compose/Dockerfile`. Adjust the Go version
  or add tooling (e.g. `grpcurl`) there as needed.

## Troubleshooting

- If `consensusd` or `p2pd` exit immediately, ensure the Docker engine has read
  and write permissions on the named volumes in `docker volume ls`.
- Swap oracle calls external APIs by default. For fully offline development,
  remove the NowPayments source from `config/swapd.yaml` and rely on
  the CoinGecko feed or stub data.
