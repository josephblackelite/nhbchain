# Escrow checkout widget and merchant demo

Two packages under `examples/escrow-checkout`, both workspace members:

| Path | Package | What it is |
| --- | --- | --- |
| `widget/` | `@nhb/escrow-checkout-widget` | React component `<EscrowCheckout />`, built with `tsup` (`yarn build`, CJS + ESM + types). |
| `merchant-demo/` | `@nhb/escrow-merchant-demo` | Express + Axios server that the widget talks to. |

## Important: the upstream API is not in this repository

The merchant demo calls an HTTP API under `/v1/escrow/...` (listed below). No server in this repository implements those routes: `services/escrow-gateway/server.go` serves no `/v1/escrow/...` path, authenticates requests with the `gateway/auth` scheme (`X-Api-Key`, `X-Timestamp`, `X-Nonce`, `X-Signature`), and signs its webhooks with an `X-Webhook-Signature` header. The demo's paths, header names, signature format and webhook verification (below) differ from that. Treat the demo as a client written against a different API contract; it will not work against `services/escrow-gateway` unchanged. For the gateway that does exist, see [`docs/escrow/nhbchain-escrow-gateway.md`](../escrow/nhbchain-escrow-gateway.md).

## Install and run

```bash
cd examples
yarn install
cd escrow-checkout/merchant-demo
yarn dev        # tsx watch src/server.ts
```

Other scripts: `yarn build` (`tsc -p tsconfig.json`), `yarn start` (`node dist/server.js`). `lint` and `test` are placeholders (`echo 'TODO...'`).

## `<EscrowCheckout />`

```tsx
import { EscrowCheckout } from '@nhb/escrow-checkout-widget';

<EscrowCheckout
  merchantBaseUrl="http://localhost:4000"
  orderId="ORDER-12345"
  customerWalletAddress="<buyer address>"
  expectedAmount={{ currency: 'NHB', value: '125.00' }}
  onStatusChange={(next) => console.log('escrow status changed to', next)}
/>
```

Props (`EscrowCheckoutProps` in `widget/src/EscrowCheckout.tsx`):

| Prop | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `merchantBaseUrl` | `string` | yes | | Base URL of the merchant server; one trailing `/` is trimmed. |
| `orderId` | `string` | yes | | Sent to the merchant server, which uses it as the idempotency key when creating the session. |
| `customerWalletAddress` | `string` | no | | Sent as `customerWalletAddress` in the create request. |
| `expectedAmount` | `{ currency, value }` | no | | Shown until the session returns its own `amount`. |
| `pollIntervalMs` | `number` | no | `5000` | Session refresh interval. |
| `autoCreate` | `boolean` | no | `true` | Create the session on mount. With `false`, call `createSession` from the controller. |
| `onStatusChange` | `(status) => void` | no | | Called when the status changes. |
| `renderHistory` | `(history) => ReactNode` | no | | Replaces the built-in history list. |
| `onController` | `(controller) => void` | no | | Receives `{createSession, refresh, markDelivered, release}`. |
| `className` | `string` | no | | Added to the container. |
| `enableMilestoneToggle` | `boolean` | no | `true` | Show the "Milestone mode" checkbox. |
| `defaultMilestoneMode` | `boolean` | no | `false` | Initial state of that checkbox; sent as `milestoneMode` when creating the session. |

Statuses (`EscrowSessionStatus`): `AWAITING_FUNDS`, `FUNDED`, `DELIVERED`, `RELEASED`, `CANCELLED`, `EXPIRED`. The "Mark as delivered" button is enabled at `FUNDED`, "Release funds" at `DELIVERED`, and the session is treated as complete at `RELEASED` or `CANCELLED`. The widget renders a QR code of the session's `paymentUri` and injects its own `<style>` element (id `nhb-escrow-checkout-styles`) with `.nhb-escrow-*` classes.

Requests the widget makes to the merchant server:

1. `POST /api/checkout/session` with `{orderId, customerWalletAddress, milestoneMode}`.
2. `GET /api/checkout/session/:sessionId`, repeated every `pollIntervalMs`.
3. `POST /api/escrow/:escrowId/deliver`.
4. `POST /api/escrow/:escrowId/release`.

## Merchant demo server

Routes (`merchant-demo/src/server.ts`): `GET /healthz` (`{ok: true}`), `POST /webhooks/escrow`, and the four `/api/...` routes above. Sessions are kept in in-memory maps, so they are lost on restart. A failed upstream call returns HTTP 502 with a short `message`.

### Configuration

Read in `merchant-demo/src/config.ts`. `apiKey`, `apiSecret`, `webhookSecret` and `walletPrivateKey` must all end up set, or startup throws `Missing escrow merchant demo configuration values`.

| Variable | Meaning |
| --- | --- |
| `NHB_API_BASE` | Upstream API base. Default `https://gw.nhbcoin.net`. |
| `NHB_API_KEY`, `NHB_API_SECRET` | Credentials for the request signature below. |
| `NHB_WEBHOOK_SECRET` | Secret for verifying incoming webhooks. |
| `NHB_WALLET_SECRET` | Base58-encoded ed25519 seed (32 bytes) or secret key (64 bytes) used to sign release requests. Any other length throws. |
| `PORT` | Listen port, default `4000`. |
| Two optional variables read in `config.ts` (a Secrets Manager secret identifier and an SSM parameter name) | Optional. Each names a stored JSON object with `apiKey`, `apiSecret`, `webhookSecret`, `walletPrivateKey`, fetched from AWS Secrets Manager or SSM Parameter Store. |
| `AWS_REGION` / `AWS_DEFAULT_REGION` | Region for those lookups, default `us-east-1`. |

Precedence is the reverse of "first source wins": the environment values are loaded first, then the Secrets Manager object is merged over them, then the SSM object is merged over that (`Object.assign` in `resolveConfig`), so a later source overrides an earlier one.

### Request signing

Every upstream request carries (`EscrowClient.buildSignature` in `src/escrowClient.ts`):

```
X-NHB-API-Key: <apiKey>
X-NHB-Timestamp: <ISO 8601 timestamp>
X-NHB-Signature: hex(HMAC-SHA256(apiSecret, `${timestamp}.${METHOD}.${path}.${jsonBody}`))
Idempotency-Key: <orderId>            (create-session only; `<orderId>-milestone` in milestone mode)
```

`path` is the request path (no base URL), `jsonBody` is the JSON string or an empty string when there is no body. Upstream calls, each returning `{ "data": ... }`:

| Call | Request |
| --- | --- |
| Create session | `POST /v1/escrow/checkout/sessions` with `{order_id, customer_wallet_address, milestone_mode}` |
| Get session | `GET /v1/escrow/checkout/sessions/{sessionId}` |
| Mark delivered | `POST /v1/escrow/escrows/{escrowId}/deliver` |
| Release | `POST /v1/escrow/escrows/{escrowId}/release` with `{wallet_address, signed_at, signature}` |

For release, `signature` is the base58 ed25519 signature of the string `${escrowId}.${signedAt}` (`signedAt` is an ISO timestamp), and `wallet_address` is the base58 encoding of the ed25519 public key (`src/signing.ts`). This is not an `nhb1...` address, and it is a different key type from the secp256k1 keys the chain uses for transactions.

Session responses are read as `session_id`, `escrow_id`, `deposit_address`, `payment_uri`, `status` (upper-cased), `expires_at`, `amount {currency, value}`, `customer.wallet_address`, `milestone_mode`, `history[]`, `milestones[]`.

### Webhooks

`POST /webhooks/escrow` is mounted with a raw body parser so the signature is computed over the exact bytes. The handler requires headers `x-nhb-signature` and `x-nhb-timestamp` and accepts the request only if the signature equals `hex(HMAC-SHA256(NHB_WEBHOOK_SECRET, `${timestamp}.${rawBody}`))` (constant-time compare, no freshness check on the timestamp). Responses: `401 invalid signature`, `400 invalid json`, `202 ignored` when `data.escrow_id` or `data.status` is missing, otherwise `200 ok`.

The handler reads this shape and ignores the event `type`:

```json
{
  "id": "<event id>",
  "type": "<event type>",
  "created_at": "<ISO 8601>",
  "data": {
    "escrow_id": "<id>",
    "status": "RELEASED",
    "note": "optional",
    "event_type": "status | milestone",
    "milestone": { "title": "optional", "amount": { "currency": "NHB", "value": "125.00" } },
    "amount": { "currency": "NHB", "value": "125.00" }
  }
}
```

A `status` event upserts the session status and appends to its history. An event counts as a milestone event when `event_type` is `milestone` or the status starts with `MILESTONE`; it appends a milestone history entry and updates the milestone list. A webhook for an escrow the server has no session for is logged and acknowledged with `200`.

## Local test

1. Start the merchant demo with the variables above set.
2. Render `<EscrowCheckout merchantBaseUrl="http://localhost:4000" ... />` in any React app.
3. Point `NHB_API_BASE` at an API that implements the `/v1/escrow/...` routes listed above.
