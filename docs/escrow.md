# NHBChain Escrow & P2P – Developer Guide

> Version: v0 (Phase 1–4) • ChainID: **187001** • HRPs: **nhb**, **znhb**
> Status: **Beta** (core state machine stable; APIs additive)

## Table of Contents
1. [Overview](#1-overview)
2. [State Machine & Data Model](#2-state-machine--data-model)
3. [Node JSON-RPC (Escrow & P2P Trade)](#3-node-json-rpc-escrow--p2p)
4. [Escrow Gateway (REST)](#4-escrow-gateway-rest)
5. [CLI (`nhb-cli`) Command Reference](#5-cli--nhb-cli-escrow--p2p)
6. [Events](#6-events)
7. [Security & Abuse Prevention](#7-security--abuse-prevention)
8. [End-to-End Examples](#8-end-to-end-examples)
9. [Errors & Return Codes](#9-errors--return-codes)
10. [Versioning](#10-versioning)
11. [Appendices](#11-appendices)

---

## 1) Overview

Escrow is a deterministic, idempotent module that **holds funds in a module vault** until a terminal action occurs: **release**, **refund**, **expire**, or **dispute/resolve**. Supports **NHB** and **ZNHB** tokens. For P2P trades, the module supports **dual-lock (two-leg) atomic settlement**, allowing buyer and seller to fund their respective legs and release simultaneously.

**Highlights**

* **Deterministic transitions:** Each status change follows strict predicates ensuring replay safety and auditability.
* **Programmable deadlines:** Deadlines enforce time-bound funding and resolution. Expiry transitions refund funds automatically when conditions are met.
* **Mediator / Arbitrator support:** Optional mediator can be set at escrow creation; arbitrators (role-based) can resolve disputes.
* **Metadata hashing:** Arbitrary metadata (JSON, reference numbers) hashed into the escrow record (`MetaHash`), enabling tamper-evident linkage to off-chain records.

---

## 2) State Machine & Data Model

### Escrow data structure

```go
type EscrowStatus uint8
const (
  EscrowInit EscrowStatus = iota
  EscrowFunded
  EscrowReleased
  EscrowRefunded
  EscrowExpired
  EscrowDisputed
)

type Escrow struct {
  ID        [32]byte
  Payer     [20]byte
  Payee     [20]byte
  Mediator  [20]byte // optional
  Token     string   // "NHB" | "ZNHB"
  Amount    *big.Int
  FeeBps    uint32
  Deadline  int64
  CreatedAt int64
  MetaHash  [32]byte
  Status    EscrowStatus
}
```

* **Vaults:** token-specific module accounts (`ESCROW_VAULT_NHB`, `ESCROW_VAULT_ZNHB`).
* **Storage:**
  * `escrow/<id>` → serialized `Escrow`.
  * `escrow/bal/<id>/<token>` → escrowed amount.
  * `escrow/history/<id>/<seq>` → transition log (for audit replay).

### P2P dual-lock trade structure

```go
type TradeStatus uint8
const (
  TradeInit TradeStatus = iota
  TradePartialFunded
  TradeFunded
  TradeDisputed
  TradeSettled
  TradeCancelled
  TradeExpired
)

type Trade struct {
  ID           [32]byte
  OfferID      string
  Buyer, Seller [20]byte
  QuoteToken   string
  QuoteAmount  *big.Int
  EscrowQuote  [32]byte
  BaseToken    string
  BaseAmount   *big.Int
  EscrowBase   [32]byte
  Deadline     int64
  CreatedAt    int64
  Status       TradeStatus
}
```

* `Trade` references two underlying escrows (`EscrowBase`, `EscrowQuote`).
* Atomic settlement ensures both legs release funds atomically; failure reverts entire operation.
* Partial funding updates status to `TradePartialFunded`. Deadline monitors auto-refund path.

### Fee handling

* `FeeBps` is applied to the escrow amount. Fees are routed to the configured fee collector. Configure via governance parameters.
* P2P offers can specify distinct fee schedules per leg or rely on default module parameters.

---

## 3) Node JSON-RPC (Escrow & P2P)

Escrow RPC methods are exposed via the node’s JSON-RPC endpoint. Write methods require signed transactions; read methods (`escrow_get`, `p2p_getTrade`) are free queries.

### Authentication / Transaction flow

1. Fetch account nonce using standard account query (e.g., `eth_getTransactionCount`).
2. Construct the method call with encoded parameters.
3. Sign with wallet (keystore, Ledger, or CLI) and broadcast via `eth_sendRawTransaction`.
4. Monitor transaction hash for inclusion; events confirm state transitions.

### Single-escrow methods

#### `escrow_create(payerBech32, payeeBech32, token, amount, feeBps, deadline, mediatorBech32|null, metaHex|null) -> {id}`
* Creates an escrow record and returns `id` plus derived payment intent (if requested via optional flag `returnPayIntent=true`).
* `metaHex` should be a hex-encoded keccak256 hash of a canonical JSON blob.
* Caller must be payer unless governance allows third-party creation (e.g., custodial services).

#### `escrow_fund(id, payerBech32) -> {ok:true}`
* Used when escrow creation did not automatically collect funds (e.g., manual transfer). Typically executed by watchers after verifying on-chain deposit.

#### `escrow_release(id, callerBech32) -> {ok:true}`
* Allowed callers: payee or mediator. Releases funds to payee minus fees; updates status to `EscrowReleased`.

#### `escrow_refund(id, callerBech32) -> {ok:true}`
* Caller must be payer; permitted only when escrow is `EscrowFunded`, before `deadline`, and not disputed.

#### `escrow_expire(id) -> {ok:true}`
* Public method. After `deadline` if escrow is funded and undisputed, automatically refunds payer and emits `escrow.expired`.

#### `escrow_dispute(id, callerBech32) -> {ok:true}`
* Caller: payer or payee. Sets status to `EscrowDisputed` and freezes release/refund transitions until resolved.

#### `escrow_resolve(id, callerBech32, outcome) -> {ok:true}`
* Requires `caller` to hold `ROLE_ARBITRATOR`.
* `outcome`: `release` or `refund`. Module enforces finality (cannot change after execution).

#### `escrow_get(id) -> EscrowJSON`
* Returns full escrow object, including computed fields: `status`, `amount`, `deadline`, `payer`, `payee`, `mediator`, `feeBps`, `events` (last N events).

### P2P dual-lock methods

#### `p2p_createTrade(offerId, buyerBech32, sellerBech32, baseToken, baseAmount, quoteToken, quoteAmount, deadline)
  -> {tradeId, escrowBaseId, escrowQuoteId, payIntents:{buyer, seller}}`
* Generates two escrows and returns pay intents used for funding each side.
* `offerId` links to off-chain listing metadata. Keep consistent for analytics.

#### `p2p_getTrade(tradeId) -> TradeJSON`
* Includes aggregated status of both escrows, funding statuses, and dispute notes.

#### `p2p_settle(tradeId, callerBech32) -> {ok:true}`
* Caller must be buyer or seller; settlement executes only after both have supplied settlement confirmations (two-phase commit).
* Implementation typically requires off-chain signatures captured via gateway (see REST section) or CLI flags.

#### `p2p_dispute(tradeId, callerBech32, message) -> {ok:true}`
* Caller: buyer or seller. Stores `message` (hash) for dispute record. Underlying escrows move to `EscrowDisputed`.

#### `p2p_resolve(tradeId, arbitratorBech32, outcome) -> {ok:true}`
* Arbitrator outcomes: `release_both`, `refund_both`, `release_base_refund_quote`, `release_quote_refund_base`.
* Module enforces invariants: both escrows must be funded; settlement occurs in one atomic transaction.

### JSON-RPC example

```bash
curl -s http://127.0.0.1:8545 -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0",
  "id":1,
  "method":"escrow_get",
  "params":["0x<escrowID>"]
}' | jq
```

---

## 4) Escrow Gateway (REST)

The Escrow Gateway is the public-facing REST service for creating and managing escrows in hosted environments. All mutating endpoints require API key + HMAC headers and an `Idempotency-Key`. Privileged endpoints additionally require wallet signatures.

### Common headers

| Header | Requirement | Notes |
|--------|-------------|-------|
| `X-Api-Key` | Required | Identify the client application. |
| `X-Timestamp` | Required | RFC3339; skew ±300s enforced. |
| `X-Signature` | Required | Hex HMAC-SHA256 of `method|path|body|timestamp`. |
| `Idempotency-Key` | Required for POST/PUT | Unique per request to ensure at-most-once semantics. |
| `X-Sig-Addr` | Required for privileged POSTs | Wallet address authorizing the action. |
| `X-Sig` | Required for privileged POSTs | EIP-191 signature over canonical payload. |

### Escrow endpoints

#### `POST /escrow/create`

* **Body**
  ```json
  {
    "payer": "nhb1payer...",
    "payee": "nhb1payee...",
    "token": "NHB",
    "amount": "1000000000000000000",
    "feeBps": 0,
    "deadline": 1730000000,
    "mediator": "nhb1mediator...",
    "meta": {"reference": "ORDER-123", "notes": "Invoice 456"}
  }
  ```
* **Response**
  ```json
  {
    "escrowId": "0xabc123...",
    "status": "created",
    "payIntent": {
      "vault": "nhb1escrowvault...",
      "token": "NHB",
      "amount": "1000000000000000000",
      "memo": "ESCROW:0xabc123...",
      "qr": "znhb://pay?to=nhb1escrowvault...&token=NHB&amount=1000000000000000000&memo=ESCROW:0xabc123..."
    }
  }
  ```
* Optional query `?returnEvents=true` includes recent events for UI initialization.

#### `GET /escrow/{id}`

* Returns escrow summary plus latest 10 events. Include `?expand=history` to fetch full transition log (paginated).

#### `POST /escrow/release`

* **Body**: `{ "escrowId": "0xabc123..." }`
* Requires wallet signature headers proving caller is payee or mediator.
* Response: `{ "escrowId": "0xabc123...", "status": "released", "txHash": "0x..." }`.

#### `POST /escrow/refund`

* **Body**: `{ "escrowId": "0xabc123..." }`
* Requires wallet signature of payer.

#### `POST /escrow/dispute`

* **Body**: `{ "escrowId": "0xabc123...", "reason": "goods not received" }`
* Caller: payer or payee. Reason string hashed before storing on-chain.

#### `POST /escrow/resolve`

* **Body**
  ```json
  {
    "escrowId": "0xabc123...",
    "outcome": "release",
    "evidence": {
      "ticket": "SUP-9182"
    }
  }
  ```
* Requires arbitrator signature (wallet with `ROLE_ARBITRATOR`).

### P2P endpoints

#### `POST /p2p/offers`

* Seller lists an offer. Body includes `baseToken`, `quoteToken`, `minAmount`, `maxAmount`, `price`, optional KYC tier requirements.
* Response returns `offerId` used in accept flow.

#### `GET /p2p/offers`

* Query parameters: `token`, `seller`, `status`, pagination (`limit`, `cursor`).

#### `POST /p2p/accept`

* **Body**
  ```json
  {
    "offerId": "OFF_123",
    "buyer": "nhb1buyer...",
    "amount": "2000000000000000000"
  }
  ```
* Response contains trade info and pay intents for both buyer and seller:
  ```json
  {
    "tradeId": "0xtrade...",
    "escrowBaseId": "0xbase...",
    "escrowQuoteId": "0xquote...",
    "payIntents": {
      "seller": {
        "to": "nhb1escrowvault...",
        "token": "NHB",
        "amount": "2000000000000000000",
        "memo": "ESCROW:0xbase...",
        "qr": "znhb://pay?to=...&token=NHB&amount=2000000000000000000&memo=ESCROW:0xbase..."
      },
      "buyer": {
        "to": "nhb1escrowvault...",
        "token": "ZNHB",
        "amount": "2000000000000000000",
        "memo": "ESCROW:0xquote...",
        "qr": "znhb://pay?to=...&token=ZNHB&amount=2000000000000000000&memo=ESCROW:0xquote..."
      }
    }
  }
  ```

#### `GET /p2p/trades/{tradeId}`

* Returns trade status, funding completeness, dispute metadata, deadlines.

#### `POST /p2p/trades/{tradeId}/settle`

* Requires signatures from both buyer and seller (`X-Sig-Addr-Buyer`, `X-Sig-Buyer`, etc.) or a combined multi-signature payload depending on deployment configuration.
* Body may include offline signed statements captured via CLI.

### PayIntent URI scheme

```
znhb://pay?to=<vault>&token=<NHB|ZNHB>&amount=<wei>&memo=ESCROW:<idhex>
```

* QR codes embed this URI for wallet apps to initiate payment automatically.
* Memo is critical for watchers to associate deposits with escrows; do not modify format.

### Rate limiting & throttling

* Default: `120` create requests/hour per API key; `600` GET requests/minute.
* Burst limiters (token bucket) allow short spikes. When a request is throttled, server returns `429` with `Retry-After` seconds.
* Monitor `X-RateLimit-Remaining` headers to plan request pacing.

### Idempotency semantics

* `Idempotency-Key` is stored alongside hashed request body. Replays with identical body return cached response; mismatched bodies yield `409` `IDEMPOTENCY_BODY_MISMATCH`.

---

## 5) CLI – `nhb-cli` (escrow & p2p)

`nhb-cli` exposes escrow management workflows for operators and support teams. Use `--help` on each subcommand for option details.

```bash
# Escrow lifecycle
nhb-cli escrow create --payer nhb1... --payee nhb1... --token NHB --amount 100e18 --deadline +72h --fee-bps 100
nhb-cli escrow get --id 0x...
nhb-cli escrow release --id 0x... --caller nhb1...
nhb-cli escrow refund  --id 0x... --caller nhb1...
nhb-cli escrow dispute --id 0x... --caller nhb1...
nhb-cli escrow resolve --id 0x... --caller nhb1... --outcome release

# Batch funding status check
nhb-cli escrow list --status funded --limit 50

# P2P operations
nhb-cli p2p offers create --seller nhb1... --base NHB --quote ZNHB --price 1.0 --min 10e18 --max 100e18
nhb-cli p2p offers list --seller nhb1...
nhb-cli p2p accept --offer OFF_123 --buyer nhb1... --amount 25e18
nhb-cli p2p trades get --id 0x...
nhb-cli p2p trades settle --id 0x... --buyer-sig <0x...> --seller-sig <0x...>
```

**Automation tips**

* Use `--output json` to integrate with scripts; combine with `jq`.
* For long-running operations, include `--timeout` to adjust RPC waiting period.
* Provide `--chain-id 187001` and `--node https://rpc.devnet.nhbchain.io` for remote clusters.

---

## 6) Events

| Event | Description | Key fields |
|-------|-------------|------------|
| `escrow.created` | Escrow created; includes payer, payee, token, amount, deadline. | `{id, payer, payee, token, amount, deadline, feeBps}` |
| `escrow.funded` | Funds detected in vault for escrow. | `{id, token, amount, txHash}` |
| `escrow.released` | Release executed. | `{id, payee, token, amount, fee, txHash}` |
| `escrow.refunded` | Refund executed. | `{id, payer, token, amount, txHash}` |
| `escrow.expired` | Expiry executed automatically. | `{id, deadline, actor}` |
| `escrow.disputed` | Dispute opened. | `{id, actor, reasonHash}` |
| `escrow.resolved` | Arbitrator resolved dispute. | `{id, outcome, arbitrator}` |
| `escrow.trade.created` | Dual-lock trade initiated. | `{tradeId, offerId, buyer, seller, baseToken, quoteToken}` |
| `escrow.trade.partial_funded` | One leg funded. | `{tradeId, fundedLeg}` |
| `escrow.trade.funded` | Both legs funded. | `{tradeId}` |
| `escrow.trade.disputed` | Trade dispute opened. | `{tradeId, actor}` |
| `escrow.trade.resolved` | Arbitrator outcome recorded. | `{tradeId, outcome, arbitrator}` |
| `escrow.trade.settled` | Atomic settlement executed. | `{tradeId, baseAmount, quoteAmount, txHash}` |
| `escrow.trade.expired` | Trade auto-expired due to deadline. | `{tradeId, deadline}` |
| `escrow.trade.cancelled` | Offer cancelled before funding. | `{tradeId, actor}` |

Events include block height and timestamp; index them for dashboards or alerting. The Escrow Gateway can forward events to webhook endpoints or message queues (Kafka, Pub/Sub) for real-time processing.

---

## 7) Security & Abuse Prevention

### Authentication & authorization

* Wallet signatures verify the identity of parties performing release/refund/resolve actions. Ensure signature payload includes timestamp and idempotency key to prevent replay.
* API keys map to organizational accounts. Rotate keys on a 90-day cadence. Use environment-specific keys (dev, staging, prod).

### Idempotency & replay protection

* All POST endpoints require `Idempotency-Key`. The server caches response bodies for 24 hours to handle retries gracefully.
* Timestamp validation rejects requests beyond configured skew window.

### Rate limiting & throttling

* Per-key rate limits mitigate brute-force or denial-of-service attacks. Monitor metrics for sustained high usage; coordinate limit increases through support channels.

### Dispute abuse safeguards

* Limit number of disputes opened per account per day via governance parameters.
* Implement off-chain heuristics (e.g., velocity checks, KYC tiers) to flag suspicious behavior.

### Compliance & logging

* Gateway maintains append-only audit logs with HMAC of request/response pairs. Logs should be replicated to immutable storage (e.g., WORM bucket).
* Arbitrator actions trigger alerts to compliance teams via webhook or SIEM integration.
* Sensitive metadata (e.g., PII) must be hashed before storage. Use salted hashing similar to loyalty user mappings.

### Key management

* Escrow vault keys are module-controlled; operators cannot withdraw outside protocol rules.
* Operator wallets (for arbitrator or mediator actions) should leverage HSM or KMS signing. Avoid exporting plaintext keys.

---

## 8) End-to-End Examples

### A. Simple escrow (cURL + CLI)

1. **Create escrow via REST**
   ```bash
   curl -s "$GATEWAY/escrow/create" \
     -H "Content-Type: application/json" \
     -H "X-Api-Key: $API_KEY" \
     -H "X-Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)" \
     -H "Idempotency-Key: $(uuidgen)" \
     -H "X-Signature: $(./scripts/sign-hmac.sh post /escrow/create '{"payer":"nhb1...","payee":"nhb1...","token":"NHB","amount":"1000000000000000000","feeBps":0,"deadline":1730000000}')" \
     -d '{"payer":"nhb1...","payee":"nhb1...","token":"NHB","amount":"1000000000000000000","feeBps":0,"deadline":1730000000}'
   ```

2. **Fund escrow** using returned PayIntent in wallet or CLI deposit command.

3. **Release funds** once goods delivered:
   ```bash
   curl -s "$GATEWAY/escrow/release" \
     -H "Content-Type: application/json" \
     -H "X-Api-Key: $API_KEY" \
     -H "X-Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)" \
     -H "Idempotency-Key: REL-$(uuidgen)" \
     -H "X-Signature: $(./scripts/sign-hmac.sh post /escrow/release '{"escrowId":"0xabc123..."}')" \
     -H "X-Sig-Addr: nhb1payee..." \
     -H "X-Sig: $(nhb-cli tx sign-payload --from nhb1payee... --payload release:0xabc123...)" \
     -d '{"escrowId":"0xabc123..."}'
   ```

4. **Verify status** via CLI: `nhb-cli escrow get --id 0xabc123...`.

### B. P2P trade settlement

1. **Seller posts offer** via CLI.
2. **Buyer accepts** via REST `POST /p2p/accept`; collects pay intents.
3. **Both parties fund** via wallet using PayIntent QR codes.
4. **Settle trade** once both legs funded:
   ```bash
   curl -s "$GATEWAY/p2p/trades/0xtrade.../settle" \
     -H "Content-Type: application/json" \
     -H "X-Api-Key: $API_KEY" \
     -H "X-Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)" \
     -H "Idempotency-Key: SET-0xtrade" \
     -H "X-Signature: $(./scripts/sign-hmac.sh post /p2p/trades/0xtrade.../settle '{"tradeId":"0xtrade..."}')" \
     -H "X-Sig-Addr-Buyer: nhb1buyer..." \
     -H "X-Sig-Buyer: $(nhb-cli p2p sign-settle --trade 0xtrade... --from nhb1buyer...)" \
     -H "X-Sig-Addr-Seller: nhb1seller..." \
     -H "X-Sig-Seller: $(nhb-cli p2p sign-settle --trade 0xtrade... --from nhb1seller...)" \
     -d '{"tradeId":"0xtrade..."}'
   ```
5. **Monitor events** `escrow.trade.settled` for confirmation and `txHash` for ledger recording.

### C. Dispute workflow

1. Payer submits `/escrow/dispute` with reason.
2. Arbitrator reviews evidence off-chain; signs `/escrow/resolve` with outcome.
3. Loyalty engine (if configured) listens to `escrow.resolved` to adjust rewards accordingly.

---

## 9) Errors & Return Codes

### REST error schema

```
{
  "error": {
    "code": "ESCROW_NOT_FOUND",
    "message": "Escrow does not exist",
    "details": {"escrowId": "0xabc123..."}
  },
  "requestId": "req-01HABC..."
}
```

* `code` aligns with JSON-RPC error names for consistency.
* `details` is optional and may contain structured data for debugging.

### Common error codes

| Code | HTTP | Description | Mitigation |
|------|------|-------------|------------|
| `INVALID_BECH32` | 400 | Malformed address. | Verify HRP and checksum; ensure lowercase. |
| `ESCROW_NOT_FOUND` | 404 | Unknown escrow ID. | Ensure ID is correct; check environment. |
| `INVALID_CALLER` | 403 | Caller not authorized for action. | Provide proper wallet signature / role. |
| `PAST_DEADLINE` | 422 | Action attempted after deadline. | Create new escrow or request arbitrator intervention. |
| `ALREADY_TERMINAL` | 409 | Escrow already in terminal state. | Avoid repeating release/refund calls. |
| `TRADE_LEG_NOT_FUNDED` | 422 | Attempt to settle before both escrows funded. | Wait for funding watchers to confirm deposits. |
| `ARBITRATOR_REQUIRED` | 403 | Action requires `ROLE_ARBITRATOR`. | Use authorized wallet. |
| `RATE_LIMITED` | 429 | Too many requests. | Backoff and retry after `Retry-After`. |

### Troubleshooting tips

* **Signature failures:** Ensure canonical JSON serialization and correct ordering of headers. For multi-signature endpoints, verify both signatures cover identical payload.
* **Stuck in `EscrowFunded`:** Confirm mediator or payee initiated release; check dispute status; monitor watchers for processing delays.
* **P2P trade expired prematurely:** Validate client clocks and deadlines; ensure funding occurred before deadline minus block propagation time.
* **Webhook replay detection:** Use `X-Request-ID` and maintain deduplication store for received events.

---

## 10) Versioning

* Escrow/Loyalty modules are aligned to Phase 1–4 roadmap. Additive changes (new fields, endpoints) will not break existing integrations.
* Subscribe to release announcements for parameter updates (e.g., deadline grace periods, fee schedules).
* Maintain integration tests using sandbox/devnet (ChainID `187001`) before deploying to production clusters.

---

## 11) Appendices

### Appendix A – HMAC & Wallet Signature

See [docs/loyalty.md](./loyalty.md#appendices) for canonical HMAC and EIP-191 signing examples shared across Loyalty and Escrow services.

### Appendix B – Meta hashing example

```
meta = {
  "reference": "ORDER-123",
  "invoice": "INV-456",
  "notes": "customer provided signature"
}
canonical = json.Marshal(meta)              // sorted keys
metaHash = keccak256(canonical)
```

Store `metaHash` in `escrow_create` and archive full JSON off-chain for auditors.

### Appendix C – Sample watcher configuration

```
[watchers.escrow]
node_url = "https://rpc.devnet.nhbchain.io"
poll_interval = "2s"
confirmations = 2
metrics_namespace = "escrow_watcher"
webhook_url = "https://hooks.example.com/escrow"
```

* Watchers monitor vault balances and trigger `escrow.funded` when deposits arrive. Configure metrics emission (Prometheus, OpenTelemetry) for observability.

---
