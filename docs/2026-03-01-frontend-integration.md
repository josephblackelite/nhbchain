# NHBChain Frontend Integration Guide

This guide details the explicit instructions requested for frontend developers integrating the NHBPortal with the hardened NHBChain backend for mainnet.

## 1. Network Constraints & Rate Limiting

The mainnet RPC servers are strictly protected. The frontend must expect HTTP `429 Too Many Requests` responses under high load.
- **Global Read Limits:** IPs are bound to global read request thresholds (e.g. `nhb_getBalance`).
- **Send Transaction Limits:** `nhb_sendTransaction` is heavily limited based on account nonces.
- Make sure to implement exponential back-offs and retry mechanisms natively in the portal UI for `nhb_getLatestTransactions` or `nhb_getBalance`.

## 2. Authentication & Authorization

### Swap Configuration 
Any interaction relying on Swap endpoints (e.g., `swap_submitVoucher`, `swap_voucher_get`, `swap_voucher_list`) requires explicit gateway authentication logic:
- A valid Gateway API Key payload or JWT token in `NHB_GATEWAY_API_KEYS`.
- Signatures on submitted vouchers. The backend's HMAC verification middleware expects valid nonces to mitigate replay attacks. 
- Avoid sending raw API keys over non-TLS connections in staging. 

## 3. Websocket Finality Subscriptions

To improve UX drastically and avoid spamming `nhb_getTransactionReceipt`, use the WebSocket Subscription endpoint for block and transaction finality:
- **Endpoint:** `ws://<RPC-Gateway>/ws/finality`
- Wait for a JSON message containing `"status": "finalized"` and the matching `"txHash"`.

## 4. Parameter Safety

- The underlying constants like `BlockTimestampToleranceSeconds` form part of consensus and have been tightly adjusted to `5 seconds` on mainnet. Future blocks drifting beyond this will fail to process. Handled timestamp logic on the frontend (like the countdowns in CrushRep/NHBPortal) must assume relatively snappy block productions with strict deadlines.
- **Sanctions / Risk Verification**: `swap_submitVoucher` is bound to a sanctions checking tool. Do not assume all addresses can transact freely. Be prepared to catch and display HTTP `403 Forbidden` referencing "address blacklisted".

## 5. Security Checklists

- Use environment variables to pass API URIs and Keys.
- Never bundle validator passwords, API keys, or Identity Gateway salts inside frontend bundles (React/NextJS `.env` vs `.env.local`).
- Handle all HTTP status codes gracefully, specially `401 Unauthorized` for expired JWTs, `429 Too Many Requests` for rate limits, and `503 Service Unavailable` when the Mempool is full.
