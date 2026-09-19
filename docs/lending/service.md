# Lending Service (`lendingd`)

`lendingd` serves the `lending.v1.LendingService` gRPC API
(`proto/lending/v1/lending.proto`) and talks to the node over JSON-RPC. It
holds no lending state of its own.

## API

| RPC | Backed by |
| --- | --- |
| `GetMarket` | node `lending_getMarket`; the request's `key.symbol` is sent as the `poolId` |
| `ListMarkets` | node `lend_getPools` |
| `GetPosition` | node `lending_getUserAccount` (`NOT_FOUND` when the account has no record) |
| `SupplyAsset`, `WithdrawAsset`, `BorrowAsset`, `RepayAsset`, `DepositCollateral`, `WithdrawCollateral`, `Liquidate` | node `nhb_sendTransaction` |

Every write RPC requires `account`, `market.symbol`, `amount` and
`signed_tx_json`. `Liquidate` takes `liquidator`, `market`, `borrower` and
`signed_tx_json`. The service forwards
`signed_tx_json` to `nhb_sendTransaction` unchanged and returns the hash the
node responds with. `account`, market and amount are only validated and
logged; **the signed transaction is what authorizes the action**, and the node
recovers the signer (`services/lending/engine/node_adapter.go`). The service
never signs. See [`docs/finance/lending/rpc-api.md`](../finance/lending/rpc-api.md)
for the transaction types and payloads to sign.

The health factor the service reports is `collateralValueUsd / borrowedValueUsd`
from `lending_getUserAccount` (`0` when there is no debt)
(`services/lending/engine/health.go`). It does not apply the liquidation
threshold, so it is not the same quantity as the node's liquidation test
(`collateralValue * LiquidationThreshold >= debt * 10000`).

## Running

The entry point is `services/lendingd/main.go`:

```bash
go run ./services/lendingd -config services/lending/config.yaml
```

`-config` defaults to `services/lending/config.yaml`. That file currently
contains only `listen: ":50053"`, which is **not sufficient to start the
service**: `Load` (`services/lendingd/config/config.go`) requires TLS
material or `allow_insecure: true`, and at least one API token or mTLS common
name.

### Configuration keys (YAML)

| Key | Default | Notes |
| --- | --- | --- |
| `listen` | `:50053` | gRPC listen address |
| `node_rpc_url` | `https://127.0.0.1:8081` | node JSON-RPC endpoint |
| `node_rpc_token` | empty | bearer token sent to the node |
| `shared_secret_header` | `X-NHB-Shared-Secret` | header name for the shared secret sent to the node |
| `shared_secret_value` | empty | shared secret value |
| `rate_limit_per_min` | `120` | parsed, but `services/lendingd/main.go` does not pass it to the server |
| `tls.cert`, `tls.key` | empty | both or neither; required unless `tls.allow_insecure` |
| `tls.client_ca` | empty | enables mutual TLS (client certificates required) |
| `tls.allow_insecure` | `false` | plaintext only on a loopback listener or when `NHB_ENV=dev` |
| `auth.api_tokens` | empty | accepted API tokens |
| `auth.mtls.allowed_common_names` | empty | requires `tls.client_ca` |

At least one of `auth.api_tokens` or `auth.mtls.allowed_common_names` must be
set. Telemetry uses `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS`
and `OTEL_EXPORTER_OTLP_INSECURE`.

### Alternate entry point

`services/lending/main.go` is a second `main` for the same gRPC service,
configured by flags (`-listen`, `-node-rpc-url`, `-node-rpc-token`,
`-shared-secret-header`, `-shared-secret`, `-tls-cert`, `-tls-key`,
`-tls-client-ca`, `-allow-insecure`, `-rate-limit-per-min`, `-mtls-required`,
`-mtls-allowed-cn`) whose defaults come from `LEND_*` environment variables
(`LEND_LISTEN`, default `0.0.0.0:9444`; `LEND_NODE_RPC_URL`; and so on, see
`services/lending/config.go`). It also starts a health server and passes the
rate limit to the server.
