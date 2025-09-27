# Lending RPC API Reference

All lending endpoints follow the JSON-RPC 2.0 specification. Requests must
include an `id`, `jsonrpc: "2.0"`, the method name, and a structured `params`
object. Responses return either a `result` object or an `error` payload.

## Table of Contents

- [Market Data](#market-data)
- [Account Management](#account-management)
- [Position Actions](#position-actions)
- [Risk & Maintenance](#risk--maintenance)

---

## Market Data

### `lend_getMarket`
Retrieve real-time information about a specific lending market.

**Parameters**

```json
{
  "market": "nhb:USDC"
}
```

**Response**

```json
{
  "symbol": "nhb:USDC",
  "name": "USD Coin",
  "decimals": 6,
  "utilization": "0.72",
  "liquidity": "1450000.231",
  "totalBorrowed": "1040000.123",
  "supplyRate": "0.045",
  "borrowRate": "0.086",
  "reserveFactor": "0.15",
  "ltv": "0.8",
  "liquidationThreshold": "0.85",
  "liquidationBonus": "0.05"
}
```

### `lend_listMarkets`
Return every active lending market.

**Parameters**

```json
{}
```

**Response**

```json
[
  {
    "symbol": "nhb:USDC",
    "utilization": "0.72"
  },
  {
    "symbol": "nhb:WBTC",
    "utilization": "0.44"
  }
]
```

## Account Management

### `lend_getUserAccount`
Fetch collateral, borrow, and reward balances for a wallet.

**Parameters**

```json
{
  "address": "nhb1qexample...",
  "market": "nhb:USDC"
}
```

**Response**

```json
{
  "address": "nhb1qexample...",
  "collateral": [
    {
      "symbol": "nhb:USDC",
      "enabled": true,
      "balance": "1500.5",
      "valueUSD": "1500.5"
    }
  ],
  "borrows": [
    {
      "symbol": "nhb:NHB",
      "balance": "500.0",
      "valueUSD": "500.0",
      "interestIndex": "1.00045"
    }
  ],
  "healthFactor": "1.43",
  "borrowCapacityUSD": "1200.4",
  "borrowedUSD": "500.0"
}
```

### `lend_enableCollateral`
Enable or disable a supplied asset as collateral.

**Parameters**

```json
{
  "address": "nhb1qexample...",
  "symbol": "nhb:USDC",
  "enabled": true
}
```

**Response**

```json
{
  "txHash": "0x1234...",
  "status": "pending"
}
```

## Position Actions

### `lend_supply`
Deposit an asset into the lending market.

```json
{
  "address": "nhb1qexample...",
  "symbol": "nhb:USDC",
  "amount": "250.0"
}
```

**Response:**

```json
{
  "txHash": "0xabc...",
  "status": "pending"
}
```

### `lend_withdraw`
Redeem supplied assets back to the user.

```json
{
  "address": "nhb1qexample...",
  "symbol": "nhb:USDC",
  "amount": "100.0"
}
```

**Response:** Same as above.

### `lend_borrow`
Borrow liquidity against enabled collateral.

```json
{
  "address": "nhb1qexample...",
  "symbol": "nhb:USDC",
  "amount": "400.0"
}
```

**Response:** Standard transaction receipt.

### `lend_borrowNHBWithFee`
Borrow NHB and route a configurable fee to a third-party application.

```json
{
  "address": "nhb1qexample...",
  "amount": "250.0",
  "feeRecipient": "nhb1qdevapp...",
  "feeBps": 75
}
```

- `feeRecipient` receives `amount * feeBps / 10_000` in NHB.
- `feeBps` is capped by governance; requests above the cap return an error.

**Response:** Transaction receipt including the fee transfer log.

### `lend_repay`
Repay outstanding debt for any supported asset.

```json
{
  "address": "nhb1qexample...",
  "symbol": "nhb:USDC",
  "amount": "150.0"
}
```

### `lend_repayWithCollateral`
Atomically repay debt by seizing enabled collateral (flash close).

```json
{
  "address": "nhb1qexample...",
  "repaySymbol": "nhb:USDC",
  "collateralSymbol": "nhb:NHB",
  "amount": "50.0"
}
```

## Risk & Maintenance

### `lend_accrueInterest`
Force an accrual cycle for a market.

```json
{
  "symbol": "nhb:USDC"
}
```

### `lend_liquidate`
Repay a portion of a borrower\'s debt and claim collateral.

```json
{
  "liquidator": "nhb1qliquid...",
  "borrower": "nhb1qexample...",
  "repaySymbol": "nhb:USDC",
  "collateralSymbol": "nhb:NHB",
  "repayAmount": "75.0"
}
```

**Response**

```json
{
  "txHash": "0xliquid...",
  "status": "pending",
  "seizedCollateral": "81.75"
}
```

All transaction-style methods return a transaction hash and status. Clients
should poll `tx_getTransaction` until the transaction is finalized.
