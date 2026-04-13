# Swap Mint API

`payments-gateway` now supports the founder-grade inbound rail: a user chooses an
external crypto to pay with, receives a NOWPayments invoice, and after successful
NOWPayments settlement the gateway mints NHB to the target NHBChain wallet.

## Peg and custody model

NHB is a treasury-backed stable-value asset with the intended production rule:

* `1 NHB = 1 USD`
* users may fund an order with any supported external crypto rail
* NHB minting must follow the final USD-equivalent value recognized by the custody and
  reconciliation layer as actually received
* NHB must not be minted from the raw amount of BTC, USDT, USDC, or other crypto the
  user sent

Under the current founder treasury model, NOWPayments is the custody and settlement
rail for inbound funding. That means the authoritative mint basis is the USD-equivalent
value confirmed through the NOWPayments settlement and reconciliation flow.

## Flow

1. Request a quote from `POST /swap/quotes`
2. Create an invoice with `POST /swap/invoices`
3. Redirect the user to the returned NOWPayments URL
4. Wait for `payments-gateway` to verify the IPN/webhook and custody settlement status
5. The gateway mints NHB from the custody-recognized USD-equivalent amount and the
   wallet balance updates on-chain

Legacy paths `/quotes` and `/invoices` remain available and map to the same handlers.

## Quote request

```json
{
  "fiat": "USD",
  "mintAsset": "NHB",
  "payCurrency": "BTC",
  "amountMint": "100"
}
```

Supported request fields:

* `fiat` - quote fiat currency. Current production expectation: `USD`
* `mintAsset` - asset to mint on NHBChain. Current founder path: `NHB`
* `payCurrency` - external crypto the user will pay with via NOWPayments, for example
  `BTC`, `USDT`, or `USDC`
* `amountMint` - target mint amount. For NHB, this is treated as the intended USD-face
  amount for a 1:1 stable-value quote
* `amountFiat` - optional explicit fiat amount for non-NHB assets or external callers

## Quote response

```json
{
  "quoteId": "c7f8...",
  "fiat": "USD",
  "token": "NHB",
  "mintAsset": "NHB",
  "payCurrency": "BTC",
  "amountFiat": "100",
  "serviceFeeFiat": "1",
  "totalFiat": "101",
  "amountToken": "100",
  "estimatedPayAmount": "0.00152",
  "expiry": "2026-04-12T10:05:00Z"
}
```

Key semantics:

* `amountToken` is the intended NHB mint amount represented by the quote
* `serviceFeeFiat` is the configured gateway fee in fiat terms
* `totalFiat` is what the NOWPayments invoice is created for
* `estimatedPayAmount` is the estimated amount in the chosen `payCurrency`
* the final mint must reconcile to the USD-equivalent value the custody layer
  recognizes as received, not merely the raw external crypto amount sent by the user

## Invoice creation

```json
{
  "quoteId": "c7f8...",
  "recipient": "nhb1..."
}
```

Example response:

```json
{
  "invoiceId": "9a2d...",
  "nowpaymentsUrl": "https://nowpayments.io/payment/?iid=...",
  "mintAsset": "NHB",
  "payCurrency": "BTC"
}
```

## Security notes

* NOWPayments API keys and IPN secrets belong on the backend service only
* The wallet frontend must never hold the NOWPayments API key, IPN secret, or NHB
  minter key
* The NHB mint signing key must remain server-side, ideally in KMS/HSM-backed storage
* Genesis configuration must never contain NOWPayments credentials or mint-signing
  secrets
