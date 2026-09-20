# Lending gRPC API

`lending.v1.LendingService` is served by the lending daemon. It reads market and
position data from a node's JSON-RPC and relays caller-signed lending
transactions to the node; it holds no lending state itself. The protobuf
definitions are in [`proto/lending/v1/lending.proto`](../../proto/lending/v1/lending.proto)
(field names below are the proto snake_case names).

## Connecting

| Property | Value |
| --- | --- |
| Package / service | `lending.v1` / `LendingService` |
| Entrypoints | `services/lending/main.go` (configured by `LEND_*` environment variables and flags) and `services/lendingd/main.go` (configured by a YAML file, `-config`, default `services/lending/config.yaml`). Both register the same service. |
| Default listen address | `0.0.0.0:9444` for `services/lending`; `:50053` for `services/lendingd` |
| Node RPC it calls | `LEND_NODE_RPC_URL` / `node_rpc_url`, default `https://127.0.0.1:8081` |

gRPC reflection is not registered.

### TLS and authentication

* TLS is required unless insecure mode is enabled (`--allow-insecure` /
  `LEND_ALLOW_INSECURE` / `tls.allow_insecure`). Insecure mode is accepted only on
  a loopback listener or when `NHB_ENV=dev`.
* The seven mutating methods (`SupplyAsset`, `WithdrawAsset`, `BorrowAsset`,
  `RepayAsset`, `DepositCollateral`, `WithdrawCollateral`, `Liquidate`) require
  authentication: an API token in the `authorization: Bearer <token>` or
  `x-api-token` metadata, or a verified mTLS client certificate (optionally
  restricted by allowed common names). Tokens come from the shared secret
  (`LEND_SHARED_SECRET` / `--shared-secret`), `LEND_API_TOKEN`, or
  `auth.api_tokens` in the YAML file. If `LEND_API_TOKEN` is set, only a token
  matches. `services/lending` refuses to start without a token or mTLS
  configuration.
* `GetMarket`, `ListMarkets` and `GetPosition` do not go through the
  authentication interceptor (`isMsgMethod` in `services/lending/server/auth.go`).
* `services/lending` limits requests with `--rate-limit-per-min` /
  `LEND_RATE_PER_MIN` (default 120).

Local plaintext example (`services/lending`):

```bash
export LEND_ALLOW_INSECURE=true
export LEND_SHARED_SECRET=devtoken
export LEND_NODE_RPC_URL=http://127.0.0.1:8545
export LEND_LISTEN=127.0.0.1:9444
go run ./services/lending
```

Because reflection is off, pass the proto file to `grpcurl`:

```bash
grpcurl -plaintext -import-path proto -proto lending/v1/lending.proto \
  127.0.0.1:9444 lending.v1.LendingService/ListMarkets
```

## Messages

### `Market`

| Field | Description |
| --- | --- |
| `key.symbol` | The lending pool id. |
| `base_asset` | Always `"NHB"` in the current server. |
| `collateral_factor` | The pool's `MaxLTV` risk parameter as an integer string (basis points). |
| `reserve_factor` | The pool's reserve factor as an integer string. |
| `liquidity_index` | The pool's supply index. |
| `borrow_index` | The pool's borrow index. |

### `AccountPosition`

| Field | Description |
| --- | --- |
| `account` | Bech32 address. |
| `supplied` | Sum of the account's supplied amounts across pools (wei). |
| `borrowed` | Sum of the account's flexible-rate borrowed amounts across pools (wei). |
| `collateral` | ZNHB collateral in wei. |
| `health_factor` | Collateral value divided by borrowed value, as a decimal string; `"0"` when the account has no debt (`engine.ComputeHealthFactor`). |

## Read methods

* `ListMarkets(ListMarketsRequest{}) -> markets[]`. Backed by the node's
  `lend_getPools`.
* `GetMarket({key: {symbol}}) -> market`. Backed by `lending_getMarket`.
* `GetPosition({account}) -> position`. Backed by `lending_getUserAccount`; an
  account with no position returns `NOT_FOUND` (`position not found`).

```bash
grpcurl -plaintext -d '{"key": {"symbol": "<pool id>"}}' \
  127.0.0.1:9444 lending.v1.LendingService/GetMarket
```

## Mutating methods

Every mutating method takes the caller's own fully signed transaction, encoded as
JSON in `signed_tx_json`, in the shape accepted by `nhb_sendTransaction`
([rpc.md](./rpc.md#transaction-encoding)). The daemon relays it unchanged; the
node recovers the signer from the transaction signature. The other request
fields are only checked for presence: `account`, `market.symbol`, `amount` and
`signed_tx_json` are all required (`account required`, `market symbol required`,
`amount required`, `signed transaction required`). `Liquidate` requires
`liquidator`, `borrower` and `signed_tx_json` (`market` is passed through but not
required); the liquidator's identity comes from the signature.

The response of each is `{tx_hash}`: the mempool-accepted transaction hash. There
is no updated position in the response; read it with `GetPosition` after the
transaction is included in a block.

| RPC | Request fields |
| --- | --- |
| `SupplyAsset`, `WithdrawAsset`, `BorrowAsset`, `RepayAsset`, `DepositCollateral`, `WithdrawCollateral` | `account`, `market`, `amount`, `signed_tx_json` |
| `Liquidate` | `liquidator`, `market`, `borrower`, `signed_tx_json` |

## Error codes

From `services/lending/server` (`server.go`, `errors.go`, `auth.go`):

| gRPC code | When |
| --- | --- |
| `INVALID_ARGUMENT` | Missing request fields; `invalid amount` (including a bad `signed_tx_json`). |
| `NOT_FOUND` | Unknown resource; `GetPosition` for an account with no position. |
| `UNAVAILABLE` | `operation paused`. |
| `PERMISSION_DENIED` | Engine reported `unauthorized`. |
| `RESOURCE_EXHAUSTED` | `insufficient collateral`. |
| `UNAUTHENTICATED` | A mutating call without valid credentials (`authentication required`, `bearer token required`, `mtls client certificate required`). |
| `FAILED_PRECONDITION` | The service has no engine configured (`lending engine unavailable`). |
| `INTERNAL` | Any other engine error (`internal error`). |
