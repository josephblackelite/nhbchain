# Lending gRPC Gateway

The gateway exposes a JSON-over-HTTP surface for the [`lending.v1.LendingService`](../../proto/lending/v1/lending.proto).
Each endpoint maps directly to a gRPC method and forwards requests to `lendingd` using the same authentication and
rate-limiting middleware as the legacy REST endpoints.

All requests and responses use JSON encodings of the underlying protobuf messages. Examples below illustrate the wire
format and reference the relevant message types.

## List markets

- **Method**: `GET /v1/lending/markets`
- **gRPC method**: `ListMarkets(ListMarketsRequest) returns (ListMarketsResponse)`
- **Description**: Retrieves the catalog of supported lending markets.

**Response example (`ListMarketsResponse`)**

```json
{
  "markets": [
    {
      "key": {"symbol": "nhb"},
      "baseAsset": "unhb",
      "collateralFactor": "0.5",
      "reserveFactor": "0.1",
      "liquidityIndex": "1.000000000000000000",
      "borrowIndex": "1.000000000000000000"
    }
  ]
}
```

## Get market

- **Method**: `POST /v1/lending/markets/get`
- **gRPC method**: `GetMarket(GetMarketRequest) returns (GetMarketResponse)`
- **Description**: Fetches a single market by its symbol.

**Request example (`GetMarketRequest`)**

```json
{
  "key": {"symbol": "nhb"}
}
```

**Response example (`GetMarketResponse`)**

```json
{
  "market": {
    "key": {"symbol": "nhb"},
    "baseAsset": "unhb",
    "collateralFactor": "0.5",
    "reserveFactor": "0.1",
    "liquidityIndex": "1.000000000000000000",
    "borrowIndex": "1.000000000000000000"
  }
}
```

## Get position

- **Method**: `POST /v1/lending/positions/get`
- **gRPC method**: `GetPosition(GetPositionRequest) returns (GetPositionResponse)`
- **Description**: Returns the current lending position for a borrower account.

**Request example (`GetPositionRequest`)**

```json
{
  "account": "nhb1exampleaccount"
}
```

**Response example (`GetPositionResponse`)**

```json
{
  "position": {
    "account": "nhb1exampleaccount",
    "supplied": "1000",
    "borrowed": "250",
    "collateral": "400",
    "healthFactor": "1.8"
  }
}
```

## Supply asset

- **Method**: `POST /v1/lending/supply`
- **gRPC method**: `SupplyAsset(SupplyAssetRequest) returns (SupplyAssetResponse)`
- **Description**: Supplies liquidity to a market.

**Request example (`SupplyAssetRequest`)**

```json
{
  "account": "nhb1exampleaccount",
  "market": {"symbol": "nhb"},
  "amount": "500"
}
```

**Response example (`SupplyAssetResponse`)**

```json
{
  "position": {
    "account": "nhb1exampleaccount",
    "supplied": "1500",
    "borrowed": "250",
    "collateral": "400",
    "healthFactor": "2.1"
  }
}
```

## Withdraw asset

- **Method**: `POST /v1/lending/withdraw`
- **gRPC method**: `WithdrawAsset(WithdrawAssetRequest) returns (WithdrawAssetResponse)`
- **Description**: Withdraws previously supplied liquidity.

**Request example (`WithdrawAssetRequest`)**

```json
{
  "account": "nhb1exampleaccount",
  "market": {"symbol": "nhb"},
  "amount": "200"
}
```

**Response example (`WithdrawAssetResponse`)**

```json
{
  "position": {
    "account": "nhb1exampleaccount",
    "supplied": "1300",
    "borrowed": "250",
    "collateral": "400",
    "healthFactor": "1.9"
  }
}
```

## Borrow asset

- **Method**: `POST /v1/lending/borrow`
- **gRPC method**: `BorrowAsset(BorrowAssetRequest) returns (BorrowAssetResponse)`
- **Description**: Borrows from a market using the caller's collateral.

**Request example (`BorrowAssetRequest`)**

```json
{
  "account": "nhb1exampleaccount",
  "market": {"symbol": "nhb"},
  "amount": "100"
}
```

**Response example (`BorrowAssetResponse`)**

```json
{
  "position": {
    "account": "nhb1exampleaccount",
    "supplied": "1300",
    "borrowed": "350",
    "collateral": "400",
    "healthFactor": "1.6"
  }
}
```

## Repay asset

- **Method**: `POST /v1/lending/repay`
- **gRPC method**: `RepayAsset(RepayAssetRequest) returns (RepayAssetResponse)`
- **Description**: Repays an outstanding borrowed amount.

**Request example (`RepayAssetRequest`)**

```json
{
  "account": "nhb1exampleaccount",
  "market": {"symbol": "nhb"},
  "amount": "75"
}
```

**Response example (`RepayAssetResponse`)**

```json
{
  "position": {
    "account": "nhb1exampleaccount",
    "supplied": "1300",
    "borrowed": "275",
    "collateral": "400",
    "healthFactor": "1.8"
  }
}
```

> **Note**
>
> The gateway validates JSON bodies against the protobuf schemas and surfaces gRPC errors using HTTP status codes.
> Refer to [`proto/lending/v1/lending.proto`](../../proto/lending/v1/lending.proto) for the authoritative
> message definitions.
