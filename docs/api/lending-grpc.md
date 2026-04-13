# Lending gRPC API

The `lending.v1.LendingService` exposes the canonical lending functionality over gRPC.  This
surface is consumed by the gateway and SDKs, and is the recommended integration point for
back-end services that need real-time interactions with the lending protocol.

> The protobuf definitions referenced throughout this document live in
> [`proto/lending/v1/lending.proto`](../../proto/lending/v1/lending.proto).  Field names below use the
> canonical snake_case protobuf casing while the generated clients expose idiomatic naming for each
> language.

## Connecting to the service

| Property | Value |
| --- | --- |
| Package | `lending.v1` |
| Service | `LendingService` |
| Default port (lendingd) | `9444` |

The service requires TLS in production deployments.  Development clusters may enable plaintext
transport; when using `grpcurl` the `-plaintext` flag can be supplied to skip TLS.

### Development quickstart

The `lendingd` binary can run locally without TLS by opting into insecure mode
and supplying an API token. The example below configures a plaintext listener
bound to localhost and reuses the same shared secret for both the environment
variable and command-line flag.

```bash
export LEND_ALLOW_INSECURE=true
export LEND_SHARED_SECRET=devtoken
export LEND_NODE_RPC_URL=http://127.0.0.1:8081
export LEND_LISTEN=127.0.0.1:9444

go run ./services/lending \
  --allow-insecure \
  --shared-secret "$LEND_SHARED_SECRET" \
  --listen "$LEND_LISTEN"
```

With lendingd running locally, gRPC reflection remains disabled. Use the
compiled protobufs when invoking `grpcurl` and include the API token metadata.

```bash
grpcurl -plaintext \
  -import-path proto \
  -proto lending/v1/lending.proto \
  -H "x-api-token: $LEND_SHARED_SECRET" \
  127.0.0.1:9444 list lending.v1.LendingService
```

Every method follows the standard gRPC error model.  In addition to transport-level errors the
service returns the following codes:

- `INVALID_ARGUMENT` – malformed symbols, empty amounts, or failing validation.
- `NOT_FOUND` – unknown market symbols or accounts with no position data.
- `FAILED_PRECONDITION` – actions that would violate collateral requirements.
- `UNAUTHENTICATED` / `PERMISSION_DENIED` – the caller lacks the required credentials.
- `UNAVAILABLE` – the lending module is temporarily unable to service the request.

## Shared message types

### `Market`

| Field | Type | Description |
| --- | --- | --- |
| `key.symbol` | `string` | Market identifier, e.g. `"nhb"`. |
| `base_asset` | `string` | Denom that is supplied and borrowed. |
| `collateral_factor` | `string` | Decimal fraction of supplied value that counts as collateral. |
| `reserve_factor` | `string` | Portion of interest captured by protocol reserves. |
| `liquidity_index` | `string` | Accumulated index for supplied balances. |
| `borrow_index` | `string` | Accumulated index for borrowed balances. |

### `AccountPosition`

| Field | Type | Description |
| --- | --- | --- |
| `account` | `string` | Bech32 account address. |
| `supplied` | `string` | Total supplied principal in base units. |
| `borrowed` | `string` | Outstanding borrowed balance in base units. |
| `collateral` | `string` | Value of collateral measured in base units. |
| `health_factor` | `string` | Ratio of collateral value to borrowed value. |

## RPC reference

### `ListMarkets`

`rpc ListMarkets(ListMarketsRequest) returns (ListMarketsResponse);`

**Request fields**

| Field | Type | Description |
| --- | --- | --- |
| _(none)_ | – | The request message has no fields. |

**Response fields**

| Field | Type | Description |
| --- | --- | --- |
| `markets[]` | `Market` | Configured markets that accept supply and borrow operations. |

**Sample invocation**

```bash
grpcurl -plaintext localhost:9090 lending.v1.LendingService/ListMarkets <<'JSON'
{}
JSON
```

### `GetMarket`

`rpc GetMarket(GetMarketRequest) returns (GetMarketResponse);`

**Request fields**

| Field | Type | Description |
| --- | --- | --- |
| `key.symbol` | `string` | Market symbol to fetch (case-sensitive). |

**Response fields**

| Field | Type | Description |
| --- | --- | --- |
| `market` | `Market` | Full market definition for the requested symbol. |

**Sample invocation**

```bash
grpcurl -plaintext localhost:9090 lending.v1.LendingService/GetMarket <<'JSON'
{
  "key": {"symbol": "nhb"}
}
JSON
```

### `GetPosition`

`rpc GetPosition(GetPositionRequest) returns (GetPositionResponse);`

**Request fields**

| Field | Type | Description |
| --- | --- | --- |
| `account` | `string` | Borrower account address. |

**Response fields**

| Field | Type | Description |
| --- | --- | --- |
| `position` | `AccountPosition` | Summary of the borrower's current state. |

**Sample invocation**

```bash
grpcurl -plaintext localhost:9090 lending.v1.LendingService/GetPosition <<'JSON'
{
  "account": "nhb1exampleaccount"
}
JSON
```

### `SupplyAsset`

`rpc SupplyAsset(SupplyAssetRequest) returns (SupplyAssetResponse);`

**Request fields**

| Field | Type | Description |
| --- | --- | --- |
| `account` | `string` | Address supplying liquidity. |
| `market.symbol` | `string` | Target market symbol. |
| `amount` | `string` | Amount to supply in base units. |

**Response fields**

| Field | Type | Description |
| --- | --- | --- |
| `position` | `AccountPosition` | Updated account position after the supply transaction. |

**Sample invocation**

```bash
grpcurl -plaintext localhost:9090 lending.v1.LendingService/SupplyAsset <<'JSON'
{
  "account": "nhb1exampleaccount",
  "market": {"symbol": "nhb"},
  "amount": "500"
}
JSON
```

### `WithdrawAsset`

`rpc WithdrawAsset(WithdrawAssetRequest) returns (WithdrawAssetResponse);`

**Request fields**

| Field | Type | Description |
| --- | --- | --- |
| `account` | `string` | Address withdrawing liquidity. |
| `market.symbol` | `string` | Market symbol to withdraw from. |
| `amount` | `string` | Amount to withdraw in base units. |

**Response fields**

| Field | Type | Description |
| --- | --- | --- |
| `position` | `AccountPosition` | Updated position reflecting the withdrawal. |

**Sample invocation**

```bash
grpcurl -plaintext localhost:9090 lending.v1.LendingService/WithdrawAsset <<'JSON'
{
  "account": "nhb1exampleaccount",
  "market": {"symbol": "nhb"},
  "amount": "200"
}
JSON
```

### `BorrowAsset`

`rpc BorrowAsset(BorrowAssetRequest) returns (BorrowAssetResponse);`

**Request fields**

| Field | Type | Description |
| --- | --- | --- |
| `account` | `string` | Borrower address. |
| `market.symbol` | `string` | Market symbol to borrow from. |
| `amount` | `string` | Amount to borrow in base units. |

**Response fields**

| Field | Type | Description |
| --- | --- | --- |
| `position` | `AccountPosition` | Updated position reflecting the borrowed balance. |

**Sample invocation**

```bash
grpcurl -plaintext localhost:9090 lending.v1.LendingService/BorrowAsset <<'JSON'
{
  "account": "nhb1exampleaccount",
  "market": {"symbol": "nhb"},
  "amount": "100"
}
JSON
```

### `RepayAsset`

`rpc RepayAsset(RepayAssetRequest) returns (RepayAssetResponse);`

**Request fields**

| Field | Type | Description |
| --- | --- | --- |
| `account` | `string` | Borrower address repaying the debt. |
| `market.symbol` | `string` | Market symbol to repay. |
| `amount` | `string` | Amount to repay in base units. |

**Response fields**

| Field | Type | Description |
| --- | --- | --- |
| `position` | `AccountPosition` | Updated position after the repayment. |

**Sample invocation**

```bash
grpcurl -plaintext localhost:9090 lending.v1.LendingService/RepayAsset <<'JSON'
{
  "account": "nhb1exampleaccount",
  "market": {"symbol": "nhb"},
  "amount": "75"
}
JSON
```

## Error handling tips

- For `INVALID_ARGUMENT` responses, re-check symbol casing and ensure numeric values are encoded as
  strings containing base units (no decimals).
- `FAILED_PRECONDITION` errors usually indicate that the requested borrow or withdrawal would push
  the account below the allowed health factor.  Fetch the latest position using `GetPosition` to
  calculate a safe amount.
- When using mutual TLS or per-RPC credentials, ensure metadata headers are forwarded by your client
  library—missing authentication details typically surface as `UNAUTHENTICATED`.

