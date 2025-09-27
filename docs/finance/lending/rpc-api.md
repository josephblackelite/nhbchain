# Lending RPC API Reference

The NHBChain node exposes a JSON-RPC interface for interacting with the native
lending engine. This document describes each method, the expected request
payloads, and the shape of the responses returned by the node.

All requests **must** set `"jsonrpc": "2.0"` and provide a numeric `id`. Amount
fields are encoded as decimal strings representing wei values.

## Authentication

Mutating endpoints require a bearer token. The node reads the token from the
`NHB_RPC_TOKEN` environment variable at startup. Clients must send the token via
the `Authorization` header:

```
Authorization: Bearer <token>
```

If the token is missing or incorrect the node responds with HTTP `401` and a
JSON-RPC error.

## Market Data

### `lending_getMarket`

Return the current global market snapshot alongside the risk parameters applied
to the native pool.

**Parameters:** none

**Response:**

```json
{
  "market": {
    "totalNHBSupplied": "1250000000000000000",
    "totalSupplyShares": "1250000000000000000",
    "totalNHBBorrowed": "600000000000000000",
    "supplyIndex": "1000000000000000000",
    "borrowIndex": "1000000000000000000",
    "lastUpdateBlock": 184,
    "reserveFactor": 1000
  },
  "riskParameters": {
    "maxLTV": 7500,
    "liquidationThreshold": 8000,
    "liquidationBonus": 500,
    "circuitBreakerActive": false
  }
}
```

`market` is `null` when the pool has not been initialised yet.

## Account Data

### `lending_getUserAccount`

Fetch the persisted lending position for an address. The parameter can be either
the raw Bech32 string or an object containing an `address` field.

**Request:**

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "lending_getUserAccount",
  "params": ["nhb1qyexample..."]
}
```

**Response:**

```json
{
  "account": {
    "collateralZNHB": "300000000000000000",
    "supplyShares": "500000000000000000",
    "debtNHB": "0",
    "scaledDebt": "0"
  }
}
```

The endpoint returns HTTP `404` when the address has no recorded position.

## Position Actions

Every state-changing method returns a pseudo transaction hash that can be used
for client-side tracking or logging. The node applies the action immediately to
its local state – the hash is an opaque acknowledgement rather than an on-chain
identifier.

### `lending_supplyNHB`

Supply NHB liquidity into the pool and mint LP shares.

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "lending_supplyNHB",
  "params": [
    {
      "from": "nhb1qyexample...",
      "amount": "1000000000000000000"
    }
  ]
}
```

**Result:** `{"txHash": "0x..."}`

### `lending_withdrawNHB`

Burn LP shares and redeem the underlying NHB back to the supplier. The request
format matches `lending_supplyNHB` (`from` and `amount`).

### `lending_depositZNHB`

Lock ZNHB as collateral for a borrower.

```json
{
  "method": "lending_depositZNHB",
  "params": [
    {"from": "nhb1qyexample...", "amount": "500000000000000000"}
  ]
}
```

### `lending_withdrawZNHB`

Unlock previously deposited collateral, subject to the position remaining
healthy. Identical payload to `lending_depositZNHB`.

### `lending_borrowNHB`

Borrow NHB against enabled collateral.

```json
{
  "method": "lending_borrowNHB",
  "params": [
    {"borrower": "nhb1qyexample...", "amount": "400000000000000000"}
  ]
}
```

### `lending_borrowNHBWithFee`

Borrow NHB while routing a governance-approved developer fee to the collector
configured in the node’s `lending` settings.

```json
{
  "method": "lending_borrowNHBWithFee",
  "params": [
    {
      "borrower": "nhb1qyexample...",
      "amount": "100000000000000000"
    }
  ]
}
```

The endpoint rejects caller-supplied fee configuration. Instead, the node reads
`DeveloperFeeBps` and `DeveloperFeeCollector` from `config.toml` and validates
the collector against the governance treasury allow list. The fee amount is
computed as `amount * DeveloperFeeBps / 10_000`, forwarded to the configured
collector, and added to the borrower’s outstanding debt.

### `lending_repayNHB`

Repay outstanding NHB debt.

```json
{
  "method": "lending_repayNHB",
  "params": [
    {"from": "nhb1qyexample...", "amount": "400000000000000000"}
  ]
}
```

### `lending_liquidate`

Repay an unhealthy borrower and seize collateral at a discount.

```json
{
  "method": "lending_liquidate",
  "params": [
    {"liquidator": "nhb1qlqdtor...", "borrower": "nhb1qborrow..."}
  ]
}
```

The response again returns a `txHash` acknowledgement. Liquidations will fail if
the borrower’s health factor is above 1.0 or if the liquidator lacks sufficient
NHB to cover the debt.

## Error Handling

Errors originating from the lending engine use the message prefix
`"lending engine:"` and are reported with JSON-RPC error code `-32602`. Storage
or infrastructure failures return code `-32000` and the HTTP status will be
`500`.

