# Lending RPC API Reference

> **Note:** A dedicated `lendingd` service is now available and documents its
> gRPC API separately in [/docs/lending/service.md](../../lending/service.md).
> The legacy JSON-RPC interface described below remains supported for existing
> deployments but will be phased out in favour of the standalone service.

The NHBChain node exposes a JSON-RPC interface for interacting with the native
lending engine. This document describes each method, the expected request
payloads, and the shape of the responses returned by the node. Hardened builds
only admit the native NHB asset and its wrapped collateral form (ZNHB); requests
referencing third-party tokens are rejected at validation time.

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

Return the current market snapshot alongside the risk parameters applied to the
requested pool.

**Parameters:** optional `poolId` string. When omitted the default pool is
returned.

**Response:**

```json
{
  "market": {
    "PoolID": "default",
    "TotalNHBSupplied": "3678901123000000000000000",
    "TotalSupplyShares": "3123456789000000000000000",
    "TotalNHBBorrowed": "2623456789000000000000000",
    "SupplyIndex": "1001234567890000000",
    "BorrowIndex": "1004567891230000000",
    "ReserveFactor": 1500,
    "LastUpdateBlock": 13245678
  },
  "riskParameters": {
    "MaxLTV": 8000,
    "LiquidationThreshold": 8500,
    "LiquidationBonus": 500,
    "DeveloperFeeCapBps": 500,
    "BorrowCaps": {
      "PerBlock": "50000000000000000000",
      "Total": "12500000000000000000000000",
      "UtilisationBps": 9000
    },
    "Oracle": {
      "MaxAgeBlocks": 30,
      "MaxDeviationBps": 500
    },
    "Pauses": {
      "Supply": false,
      "Borrow": false,
      "Repay": false,
      "Liquidate": false
    },
    "CircuitBreakerActive": false
  }
}
```

`market` is `null` when the pool has not been initialised yet.

### `lend_getPools`

List the configured lending pools and their current accounting snapshots.

**Parameters:** none

**Response:**

```json
{
  "pools": [
    {"poolID": "default", "totalNHBSupplied": "0", "totalSupplyShares": "0"}
  ],
  "riskParameters": {"maxLTV": 7500, "liquidationThreshold": 8000}
}
```

### `lend_createPool`

Create a new lending pool using the node’s configured developer fee settings.

**Parameters:** object with `poolId` and `developerOwner` (Bech32) fields.

**Response:** identical to `lending_getMarket` for the newly created pool.

## Account Data

### `lending_getUserAccount`

Fetch the persisted lending position for an address. The parameter can be either
the raw Bech32 string or an object containing an `address` field. Include
`poolId` when querying non-default pools.

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
    "CollateralZNHB": "300000000000000000",
    "SupplyShares": "500000000000000000",
    "DebtNHB": "900000000000000000",
    "ScaledDebt": "903375000000000000"
  }
}
```

The endpoint returns HTTP `404` when the address has no recorded position.

## Position Actions

Every state-changing method returns a pseudo transaction hash that can be used
for client-side tracking or logging. The node applies the action immediately to
its local state – the hash is an opaque acknowledgement rather than an on-chain
identifier.

All mutating requests accept an optional `poolId` field. When omitted the
default pool is used.

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
      "amount": "1000000000000000000",
      "poolId": "default"
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
    {"from": "nhb1qyexample...", "amount": "500000000000000000", "poolId": "default"}
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
    {"borrower": "nhb1qyexample...", "amount": "400000000000000000", "poolId": "default"}
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
      "amount": "100000000000000000",
      "poolId": "default"
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
    {"from": "nhb1qyexample...", "amount": "400000000000000000", "poolId": "default"}
  ]
}
```

### `lending_liquidate`

Repay an unhealthy borrower and seize collateral at a discount.

```json
{
  "method": "lending_liquidate",
  "params": [
    {"liquidator": "nhb1qlqdtor...", "borrower": "nhb1qborrow...", "poolId": "default"}
  ]
}
```

The response again returns a `txHash` acknowledgement. Liquidations will fail if
the borrower’s health factor is above 1.0 or if the liquidator lacks sufficient
NHB to cover the debt.

## Error Handling

Validation failures surface as `codeInvalidParams` (`-32602`) with descriptive
messages such as `"invalid parameter object"`, `"invalid borrower"`, or amount
parser errors (`"amount is required"`, `"invalid amount"`,
`"amount must be positive"`). When the engine rejects a call the message retains
the prefix (for example `"lending engine: borrow exceeds per-block cap"`) and
the same text is echoed in the `data` field for display or telemetry.

Authentication issues return HTTP `401` / `codeUnauthorized` while unexpected
infrastructure failures fall back to `codeServerError` (`-32000`). Module errors
preserve their own HTTP status – hitting a borrow cap or circuit breaker returns
HTTP `400`, whereas trying to access a pool before it is initialised yields
HTTP `404`.

Example error response:

```json
{
  "jsonrpc": "2.0",
  "id": 42,
  "error": {
    "code": -32602,
    "message": "lending engine: borrow exceeds per-block cap",
    "data": "lending engine: borrow exceeds per-block cap"
  }
}
```

## Operational Controls

Two hardened levers gate the money market:

- **Kill switch.** `riskParameters.Pauses` mirrors the on-chain `system/pauses`
  map. Use the helper scripts to inspect or toggle the state during incident
  response:

  ```bash
  go run ./examples/docs/ops/read_pauses
  go run ./examples/docs/ops/pause_toggle --module lending --state pause
  ```

- **Borrow caps.** `riskParameters.BorrowCaps` enforces per-block, utilisation,
  and global ceilings. Stage overrides by editing the node configuration overlay
  and reloading:

  ```toml
  [lending.borrowCaps]
  perBlock = "75000000000000000000"
  total    = "15000000000000000000000000"
  utilisationBps = 8800
  ```

  Track utilisation in dashboards and revert to the baseline once the incident
  is resolved.

