# Escrow checkout widget + merchant demo

This example bundles a drop-in React component (`<EscrowCheckout />`) with a Node.js merchant demo server. The server calls an escrow HTTP API at `NHB_API_BASE`. The goal is to show how a merchant could run an escrow checkout: buyers fund an escrow account via QR code, the merchant confirms delivery, the seller releases funds, and webhook notifications update the UI.

> **What is and is not in this repository.** The widget and the merchant demo
> server are in `examples/escrow-checkout`. The escrow HTTP API the server calls
> (`POST /v1/escrow/checkout/sessions`, `GET /v1/escrow/checkout/sessions/<id>`,
> `POST /v1/escrow/escrows/<id>/deliver` and `.../release`), the header
> signature scheme described below, and the webhook sender are not implemented
> anywhere in this repository (a search of the Go sources finds none of these
> routes), so they cannot be checked against the code here. This page therefore
> documents only what the example's own TypeScript does. The chain's own escrow
> operations are native transaction types (`CreateEscrow`, `LockEscrow`,
> `ReleaseEscrow`, `RefundEscrow`, `DisputeEscrow` and the arbitration types;
> see `formatTxType` in `rpc/types.go`) submitted with `nhb_sendTransaction`.

```
/examples/escrow-checkout/widget          # React component package
/examples/escrow-checkout/merchant-demo   # Express + Axios server
```

Both packages are wired into the `examples` Yarn workspaces (`examples/package.json`) and can be run with the standard scripts once dependencies are installed.

## Installing dependencies

```bash
cd examples
yarn install
```

- The widget is `@nhb/escrow-checkout-widget` and is compiled with `tsup`.
- The merchant demo server (`@nhb/escrow-merchant-demo`) is a TypeScript Express application. Its code signs outgoing API requests with an HMAC, signs the release request with an ed25519 key, and verifies incoming webhooks with an HMAC.

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

Defined in `examples/escrow-checkout/widget/src/EscrowCheckout.tsx`.

| Prop | Type | Required | Description |
| --- | --- | --- | --- |
| `merchantBaseUrl` | `string` | yes | Base URL of the merchant server (the demo exposes `/api/...` routes). One trailing slash is removed. |
| `orderId` | `string` | yes | Merchant order identifier. The widget sends it to the merchant server, which forwards it as the `Idempotency-Key` when it creates the checkout session. |
| `customerWalletAddress` | `string` | no | Buyer wallet address passed to the merchant server when creating the session. |
| `expectedAmount` | `{ currency: string; value: string }` | no | Amount rendered while the session is being created. The amount from the API replaces it once the session returns. |
| `pollIntervalMs` | `number` | no | Polling cadence for session refreshes. Defaults to `5000`. |
| `autoCreate` | `boolean` | no | When `true` (default) the widget requests a session as soon as it mounts. Set to `false` to call `createSession` through the controller. |
| `enableMilestoneToggle` | `boolean` | no | Shows the Milestone Mode toggle. Defaults to `true`. |
| `defaultMilestoneMode` | `boolean` | no | Initial value of Milestone Mode before a session is created. Defaults to `false`. The current value is sent as `milestoneMode` when a session is created. |
| `onStatusChange` | `(status) => void` | no | Called every time the escrow status changes. |
| `renderHistory` | `(history) => ReactNode` | no | Custom renderer for the session history timeline. |
| `onController` | `(controller) => void` | no | Receives a controller with `createSession`, `refresh`, `markDelivered` and `release`. |
| `className` | `string` | no | Extra class name for the container. |

The widget injects its own `<style>` block the first time it renders. You can override any of the `.nhb-escrow-*` classes to match your brand.

### Session lifecycle

1. **Create session**: the widget calls `POST /api/checkout/session` on the merchant server with `{ orderId, customerWalletAddress, milestoneMode }`.
2. **Fund escrow**: the buyer scans the QR code or sends funds to the provided deposit address. The widget polls `GET /api/checkout/session/:sessionId`.
3. **Delivery**: the merchant chooses "Mark as delivered", which calls `POST /api/escrow/:escrowId/deliver`.
4. **Release**: the server calls `POST /api/escrow/:escrowId/release`. When a webhook reports the new status, the widget updates.

## Merchant demo server

The Express server (`examples/escrow-checkout/merchant-demo/src/server.ts`) exposes `GET /healthz`, `POST /webhooks/escrow` and the four `/api/...` endpoints above, and translates them into calls to the escrow HTTP API at `NHB_API_BASE` (`escrowClient.ts`).

### Environment variables

Read in `src/config.ts`.

| Variable | Description |
| --- | --- |
| `NHB_API_BASE` | Base URL of the escrow HTTP API. Defaults to the gateway URL hard-coded in `src/config.ts`. |
| `NHB_API_KEY` | API key sent in `X-NHB-API-Key`. |
| `NHB_API_SECRET` | Secret used for the request HMAC. |
| `NHB_WEBHOOK_SECRET` | Shared secret used to verify webhook signatures. |
| `NHB_WALLET_SECRET` | Base58 encoded ed25519 32-byte seed or 64-byte secret key used to sign release requests. |
| `PORT` | Port for the Express server. Defaults to `4000`. |
| `ESCROW_SECRETS_ARN` | Optional ARN of a secrets-manager secret holding a JSON object with any of `apiKey`, `apiSecret`, `webhookSecret`, `walletPrivateKey`. |
| `ESCROW_SSM_PARAMETER` | Optional parameter-store name holding the same JSON structure. |
| `AWS_REGION` | Region used to read those secrets. Falls back to `AWS_DEFAULT_REGION`, then `us-east-1`. |

`resolveConfig` reads the environment first, then merges the secrets-manager bundle, then the parameter-store bundle, each with `Object.assign`. A key present in a later source therefore overrides the same key from an earlier one (the secrets-manager bundle over the environment, the parameter-store bundle over both). The server refuses to start if `apiBase`, `apiKey`, `apiSecret`, `webhookSecret`, `walletPrivateKey` or `port` is missing after that.

### HMAC signing

Every outbound call from `EscrowClient` carries the header trio below. This is what the example code produces. No code in this repository verifies it, so ask the operator of the escrow API that `NHB_API_BASE` points at what it accepts.

```
X-NHB-API-Key: <api key>
X-NHB-Timestamp: <ISO 8601 timestamp>
X-NHB-Signature: hex HMAC_SHA256(secret, `${timestamp}.${METHOD}.${path}.${jsonBody}`)
```

`METHOD` is upper case and `jsonBody` is the empty string for requests without a body. Requests are JSON encoded. The create-session call adds an `Idempotency-Key` header equal to the order ID (`<orderId>-milestone` in milestone mode).

### Wallet signature for releases

For a release, the demo decodes the merchant's ed25519 private key from base58 (`tweetnacl`, `src/signing.ts`), signs the string `${escrowId}.${signedAt}`, and sends the base58 signature together with the base58 public key as `wallet_address`. This is an ed25519 key, not an NHB account key: NHB accounts use secp256k1 and bech32 `nhb1...` addresses, so the chain itself never verifies this signature.

```json
{
  "wallet_address": "<base58 ed25519 public key>",
  "signed_at": "2024-03-01T18:24:10.208Z",
  "signature": "<base58 signature>"
}
```

### Webhook verification

The `/webhooks/escrow` route reads the `X-NHB-Signature` and `X-NHB-Timestamp` request headers and compares the signature with the hex `HMAC_SHA256(secret, `${timestamp}.${rawBody}`)` (`src/webhooks.ts`). It answers `401 invalid signature` on a mismatch and does not check how old the timestamp is. The only `X-NHB-Signature` header that Go code in this repository produces is the outgoing rewards webhook (`integrations/webhooks/rewards.go`); nothing in this repository sends escrow webhooks.

The handler reads `data.escrow_id`, `data.status`, and optionally `data.note`, `data.event_type`, `data.milestone` and `data.amount`, and merges the event into an in-memory session store. An event for an escrow the server has not seen is logged and otherwise ignored. An event missing `escrow_id` or `status` gets `202 ignored`. Example payload:

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

## Local testing

1. Start the merchant demo server:
   ```bash
   cd examples/escrow-checkout/merchant-demo
   yarn dev
   ```
2. Run your React app that imports `@nhb/escrow-checkout-widget` and point `merchantBaseUrl` at `http://localhost:4000`.
3. This needs an escrow HTTP API at `NHB_API_BASE` that speaks the calls above. This repository does not include one.

> **Deployment note:** whatever fronts `/webhooks/escrow` must forward the raw JSON body unmodified, because the server verifies the HMAC over the raw bytes (`express.raw`). The optional `ESCROW_SECRETS_ARN` / `ESCROW_SSM_PARAMETER` variables make `config.ts` read a JSON secret bundle from a secrets manager or a parameter store.
