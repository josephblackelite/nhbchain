# NHBChain — Frontend Integration & Deployment Guide
**Date:** 2026-03-01

This document provides definitive instructions for frontend developers (e.g., NHBPortal and third-party apps) on how to securely connect to the NHBChain mainnet and deploy their frontend infrastructure securely.

---

## 1. Connecting to the RPC Network Securely

For production, your frontend application MUST NOT connect to an HTTP-only RPC node. The communication layer must be secured.

### 1.1 Setting `L1_NODE_RPC_URL`
In your environment variables, ensure the RPC URL points to the secure, load-balanced endpoint:
```env
# Example .env.production
L1_NODE_RPC_URL=https://rpc.nhbcoin.com
DEFAULT_NHB_CHAIN_ID=187001
ENABLE_RPC_STUBS=false
```

### 1.2 Removing Insecure TLS Dispatchers
If you are using NodeJS (e.g., SvelteKit backend), you must **remove all code that overrides TLS verification**.
Delete or disable code snippets like `withInsecureTlsDispatcher` (`rejectUnauthorized: false`) in your HTTP/RPC clients.

---

## 2. API Integration Best Practices

### 2.1 Implementing Real-Time Finality Streams
Polling the blockchain continuously uses excessive resources and causes 3+ second latencies. Instead:
- Connect securely to `wss://rpc.nhbcoin.com/subscribe`.
- Listen for `SubscribeFinality` events. This provides instant WebSocket notifications when POS payments, Swaps, or Transfers are mathematically finalized.

### 2.2 Swap Integration
- The frontend must orchestrate multi-step Native On-Chain Swaps (`SwapMint` and `SwapBurn`), interacting directly with the chain logic rather than off-chain redirects.
- Do not use hardcoded static fallback oracle prices. If the RPC oracle is unavailable, fail the transaction securely.

### 2.3 POS Merchant Flow
When building merchant POS UI flows:
1. Call `AuthorizePayment` to lock the user's funds.
2. Verify the product/service delivery.
3. Call `CapturePayment` to finalize the charge, or `VoidPayment` if the transaction is canceled.

---

## 3. Frontend Deployment Security Hardening

When deploying your frontend application (SvelteKit, NextJS, etc.) to AWS, Vercel, or standard VPS, enforce the following:

- **Strict Rate Limiting:** Apply per-user (by `userId` or wallet address, not shared IP) rate limits specifically on `/api/rpc` gateway routes (using Redis).
- **CSRF Protection:** Implement double-submit cookies or standard CSRF synchronization tokens on all mutation/POST endpoints.
- **CSP Headers:** Enforce strict Content Security Policy (CSP) headers configured to block XSS variants. Inject a `report-to` directive to get automated logs of violations.
- **Private Key Storage:** Ensure user wallet keys generated locally are saved into `sessionStorage` (clears upon close) or managed natively via non-extractable Web `CryptoKey` API mechanisms. Never persist them lightly to `localStorage` unless securely encrypted.
- **Sanctions & Checks:** Utilize frontend screening APIs before broadcasting signed transactions to ensure OFAC compliance.

---

## 4. Multi-Node Failover Resiliency
Configure your SDK or backend node client to accept a comma-separated list of `L1_NODE_RPC_URL` values. Provide auto-failover logic so if `rpc1.nhbcoin.com` goes down, the frontend seamlessly retries with `rpc2.nhbcoin.com` before alerting the user.
