# Escrow checkout widget + merchant demo

This example bundles a drop-in React component (`<EscrowCheckout />`) with a Node.js merchant demo server that speaks to https://api.nhbcoin.net. The goal is to show how a merchant can spin up a full escrow checkout: buyers fund an escrow account via QR code, the merchant confirms delivery, the seller releases funds, and webhook notifications update the UI end-to-end.

```
/examples/escrow-checkout/widget          # React component package
/examples/escrow-checkout/merchant-demo   # Express + Axios server
```

Both packages are wired into the `examples` Yarn workspaces and can be run with the standard scripts once dependencies are installed.

## Installing dependencies

```bash
cd examples
yarn install
```

- The widget is published as `@nhb/escrow-checkout-widget` and is compiled with `tsup`.
- The merchant demo server (`@nhb/escrow-merchant-demo`) is a TypeScript Express application that signs API requests with HMAC, produces wallet signatures for escrow release, and verifies NHB webhooks.

## Using the `<EscrowCheckout />` widget

```tsx
import React from 'react';
import { EscrowCheckout } from '@nhb/escrow-checkout-widget';

export function CheckoutPage() {
  return (
    <EscrowCheckout
      merchantBaseUrl="https://merchant-demo.example.com"
      orderId="ORDER-12345"
      customerWalletAddress="nhb1p3...buyer"
      expectedAmount={{ currency: 'NHB', value: '125.00' }}
      onStatusChange={(next) => console.log('escrow status changed to', next)}
    />
  );
}
```

### Props

| Prop | Type | Required | Description |
| --- | --- | --- | --- |
| `merchantBaseUrl` | `string` | ✅ | Base URL to the merchant server (the demo exposes `/api/...` routes). A trailing slash is automatically trimmed. |
| `orderId` | `string` | ✅ | Stable identifier for the order or cart. The widget sends this to the merchant server, which forwards it as an idempotency key when creating the escrow checkout session. |
| `customerWalletAddress` | `string` | – | Pre-fills the buyer wallet in the escrow session (if omitted the server can generate/lookup one). |
| `expectedAmount` | `{ currency: string; value: string }` | – | Optional optimistic amount rendered while the session is being created. The real amount from the API overwrites it once the checkout session returns. |
| `pollIntervalMs` | `number` | – | Polling cadence for session refreshes. Defaults to `5000` (5s). |
| `autoCreate` | `boolean` | – | When `true` (default) the widget requests a session as soon as it mounts. Set to `false` if you want to manually call `createSession` via the controller. |
| `onStatusChange` | `(status) => void` | – | Receives a callback every time the escrow status changes. |
| `renderHistory` | `(history) => ReactNode` | – | Custom renderer for the session history timeline. |
| `onController` | `(controller) => void` | – | Surfaces a controller with `createSession`, `refresh`, `markDelivered`, and `release` actions to the host application. |
| `className` | `string` | – | Optional container className for styling overrides. |

The widget injects its own `<style>` block the first time it renders. You can override any of the `.nhb-escrow-*` classes to match your brand.

### Session lifecycle

1. **Create session** – `POST /api/checkout/session` is called on the merchant server with `{ orderId, customerWalletAddress }`.
2. **Fund escrow** – The buyer scans the QR code or sends funds to the provided deposit address. The widget polls `GET /api/checkout/session/:sessionId` to capture updates.
3. **Delivery** – The merchant hits "Mark as delivered" which maps to `POST /api/escrow/:escrowId/deliver`.
4. **Release** – After a wallet signature is produced the server calls `POST /api/escrow/:escrowId/release`. Once webhooks confirm the release, the UI shows the escrow as complete.

## Merchant demo server

The Express server exposes the endpoints consumed by the widget and translates them into NHB API calls at https://api.nhbcoin.net.

### Environment variables

| Variable | Description |
| --- | --- |
| `NHB_API_BASE` | Override the NHB API base (defaults to `https://api.nhbcoin.net`). |
| `NHB_API_KEY` | API key for authenticated calls. |
| `NHB_API_SECRET` | API HMAC secret. |
| `NHB_WEBHOOK_SECRET` | Shared secret used to verify webhook signatures. |
| `NHB_WALLET_SECRET` | Base58 encoded ed25519 seed or 64-byte secret key used to sign release requests. |
| `PORT` | Port for the Express server (defaults to `4000`). |
| `ESCROW_SECRETS_ARN` | Optional AWS Secrets Manager ARN containing a JSON object with `apiKey`, `apiSecret`, `webhookSecret`, and/or `walletPrivateKey`. |
| `ESCROW_SSM_PARAMETER` | Optional AWS Systems Manager Parameter Store name containing the same JSON structure as above. |
| `AWS_REGION` | AWS region to use when reading secrets. |

Secrets are loaded in the following order: environment variables → Secrets Manager → SSM Parameter Store. The first source that provides a value wins, letting you keep production credentials in AWS while still supporting local `.env` files.

### HMAC signing

Every outbound call to NHB uses the header trio produced in `EscrowClient`:

```
X-NHB-API-Key: <api key>
X-NHB-Timestamp: <ISO 8601 timestamp>
X-NHB-Signature: HMAC_SHA256(secret, `${timestamp}.${method}.${path}.${jsonBody}`)
```

Requests are JSON encoded and include an `Idempotency-Key` header set to the order ID when the checkout session is created.

### Wallet signature for releases

To release funds the API requires a wallet signature. The demo decodes the merchant's ed25519 private key from base58, signs the string `${escrowId}.${signedAt}`, and sends the base58 signature together with the derived public key:

```json
{
  "wallet_address": "nhb1merchantpub...",
  "signed_at": "2024-03-01T18:24:10.208Z",
  "signature": "5Kf...signedpayload"
}
```

### Webhook verification

The ALB (or any HTTPS terminator) should forward NHB webhook calls to `/webhooks/escrow`. The route uses `X-NHB-Signature` and `X-NHB-Timestamp` to compute `HMAC_SHA256(secret, `${timestamp}.${rawBody}`)` before accepting the request.

Sample webhook payload:

```json
{
  "id": "evt_01HV8E7J7P7SQR34WS0F9YJ5QG",
  "type": "escrow.status.changed",
  "created_at": "2024-03-01T18:24:23.182Z",
  "data": {
    "escrow_id": "esc_01HV8E6N1D0HB0ZWC7C32MA3P1",
    "status": "RELEASED",
    "note": "Seller wallet credited",
    "amount": {
      "currency": "NHB",
      "value": "125.00"
    }
  }
}
```

The webhook handler merges events into the in-memory session store so the React widget shows a chronological history without polling every detail from the API.

## Local testing

1. Start the merchant demo server:
   ```bash
   cd examples/escrow-checkout/merchant-demo
   yarn dev
   ```
2. Run your React app (or storybook) that imports `@nhb/escrow-checkout-widget` and point `merchantBaseUrl` at `http://localhost:4000`.
3. Use the QR code to fund escrow from a wallet, then trigger delivery and release. Webhooks hitting `/webhooks/escrow` will update the widget automatically.

> **Deployment tip:** behind an AWS Application Load Balancer make sure `/webhooks/escrow` forwards the raw JSON body (no Lambda proxy that modifies payloads) so that the signature check succeeds. Provision the API/HMAC secrets in Secrets Manager or SSM and set `ESCROW_SECRETS_ARN` / `ESCROW_SSM_PARAMETER` accordingly.
