# NHBCHAIN EPIC — Escrow Gateway (REST) + Disputes/Arbitration + P2P Market Hooks

## Goals

- Expose escrow to external apps via REST/JSON while keeping on-chain funds and state authoritative.
- Restrict funding and settlement to NHB/ZNHB tokens.
- Provide deterministic dispute flows with mutual resolution or arbitrator decision.
- Offer drop-in P2P market integration that auto-creates and settles escrows.
- Deliver strong security: API keys with HMAC, participant wallet signatures for privileged actions, idempotency, rate limits, webhooks, and audit logging.

## Part A — Escrow Gateway Service (REST, Stateless, Idempotent)

### Service Skeleton

- **Location**: `services/escrow-gateway` (Go).
- **Dependencies**: node JSON-RPC (E2), storage (Postgres/sqlite), signer (HMAC), address utilities (bech32), QR generator.

### Configuration (Environment)

```text
GATEWAY_PORT=8089
GATEWAY_NODE_RPC_URL=http://localhost:8545
GATEWAY_CHAIN_ID=187001
GATEWAY_API_RATE_RPS_PER_KEY=5
GATEWAY_HMAC_ALG=HMAC_SHA256
GATEWAY_WEBHOOK_MAX_RETRY=10
```

### Authentication & Authorization

- API key + HMAC per developer/app (e.g., “usedtown”).
- Privileged actions (**release**, **dispute**, **resolve**) require participant wallet signature.
  - `X-Sig-Addr`: `nhb…`/`znhb…`.
  - `X-Sig`: `hex(eip191_sign(keccak256(method|path|body|timestamp|escrowId)))`.
  - `X-Timestamp`: RFC3339 (±300s skew).
- Gateway verifies signature matches payer or payee for that escrow, or an arbitrator address.

### Idempotency

- `Idempotency-Key` header captured with `{appKey, route, key}` for all write endpoints.

### Funding Model (NHB/ZNHB Only)

1. Funds held by on-chain vault (E1).
2. Gateway only creates on-chain escrow via node RPC and returns pay intent (address + memo/ID) plus optional QR.
3. Gateway watches chain events to update REST status & trigger webhooks.

### REST Endpoints

```
POST /escrow/create
Body: {
  "payer":   "nhb1...",
  "payee":   "nhb1...",
  "token":   "NHB" | "ZNHB",
  "amount":  "100000000000000000000",
  "feeBps":  0,
  "deadline": 1730000000,
  "mediator": "nhb1..." | null,
  "meta":     { "reference": "ORDER-123", "note":"used stove" }
}
→ 201 {
  "escrowId": "0x…32bytes…",
  "payIntent": {
    "vault": "nhb1ESCROWVAULT…",
    "memo":  "ESCROW:0x…",
    "qr":    "znhb://pay?to=nhb1ESCROWVAULT…&token=NHB&amount=100e18&memo=ESCROW:0x…"
  }
}
```

```
GET /escrow/{id}
→ {
  "status": "INIT|FUNDED|RELEASED|REFUNDED|EXPIRED|DISPUTED",
  "payer":"nhb1…",
  "payee":"nhb1…",
  "token":"NHB",
  "amount":"…",
  "deadline":…,
  "mediator":"nhb1…|null",
  "events":[...]
}
```

```
POST /escrow/release
Headers: API key, HMAC, X-Sig-Addr (payee or mediator), X-Sig, X-Timestamp, Idempotency-Key
Body: { "escrowId": "0x…", "reason":"delivered_ok" }
→ 202 { "queued": true }
```

```
POST /escrow/refund
Headers: API key, HMAC, X-Sig-Addr (payer), X-Sig, X-Timestamp
Body: { "escrowId": "0x…", "reason":"mutual_refund" }
→ 202 { "queued": true }
```

```
POST /escrow/dispute
Headers: API key, HMAC, X-Sig-Addr (payer or payee), X-Sig
Body: { "escrowId":"0x…", "message":"item damaged", "evidenceUrls":["https://..."] }
→ 202 { "ok": true }
```

```
POST /escrow/resolve
Headers: API key, HMAC, X-Sig-Addr (both parties or mediator/arbitrator), X-Sig
Body: { "escrowId":"0x…", "outcome":"release"|"refund" }
→ 202 { "queued": true }
```

```
GET /escrow/{id}/events
→ [{ "type":"escrow.funded", "ts":..., "txHash":"0x…" }, ...]
```

### Webhooks

- `escrow.created`, `escrow.funded`, `escrow.released`, `escrow.refunded`, `escrow.expired`, `escrow.disputed`, `escrow.resolved`.
- Payload: `{escrowId, payer, payee, token, amount, txHash?, meta.reference}`.

### Node Integration

- Calls node RPC (`escrow_create`, `escrow_release`, `escrow_refund`, `escrow_dispute`, `escrow_resolve`, `escrow_get`).
- Subscribes to block/events to sync status.

### Security & Abuse Mitigation

- Per-key rate limits; HMAC body integrity.
- Wallet signature proves payer/payee/arbitrator control.
- Idempotency on writes.
- Append-only audit log (request hash, actor, escrowId, node RPC result, block/tx).

### Acceptance Criteria

- Unit tests: auth (HMAC + sig), idempotency, signature mismatch, invalid bech32, deadline checks.
- Integration: create → pay → funded → release → settlement → webhook.

## Part B — P2P Market Hooks (Auto-Escrow + Arbitration)

### Offer Model

```json
{
  "offerId":"OFF_...",
  "seller":"nhb1...",
  "token":"NHB|ZNHB",
  "pricePerUnit":"...wei",
  "minAmount":"...wei",
  "maxAmount":"...wei",
  "terms":"text",
  "active": true
}
```

### Endpoints

- `POST /p2p/offers` – seller creates offer (API key + seller signature).
- `GET /p2p/offers` – list offers.
- `POST /p2p/accept` – buyer accepts, gateway creates escrow and returns pay intent & QR.

### Settlement Flow

- Buyer funds escrow; seller marks delivered.
- Buyer releases via `/escrow/release` or mediator invoked.
- Arbitrator addresses (`ROLE_ARBITRATOR`) can resolve via `/escrow/resolve`.

### Events & Acceptance

- Events: `p2p.offer.created/accepted/cancelled` with escrow lifecycle integration.
- Integration: seller creates offer → buyer accepts → funds → release → seller receives.

## Part C — Escrow Pay Intent Specification

- **Vault**: module vault bech32 per token.
- **Memo/Data**: `ESCROW:<idhex>` or ABI call `depositEscrow(bytes32 id)`.
- **QR URI**: `znhb://pay?to=<vault>&token=<NHB|ZNHB>&amount=<wei>&memo=ESCROW:<idhex>`.

## Part D — Loyalty Alignment

- Loyalty accrues on escrow release; gateway-triggered release invokes loyalty engine.
- Webhooks include `escrow.released` and optional `loyalty.program.accrued`.

## Part E — Developer SDK Stubs (Bonus)

- Directories: `sdks/js`, `sdks/go`.
- Provide helpers for signing (EIP-191), HMAC, REST client with idempotency.
- Example usage:

```ts
await client.createEscrow({
  payer, payee, token: "NHB", amount: "100000000000000000000",
  deadline: in3Days(), meta: { reference: "ORDER-123" }
});
await wallet.payQR(payIntent.qr);
await client.release({ escrowId }, { signer: buyerWallet });
```

---

# NHBCHAIN Addendum — P2P Dual-Lock Escrow (Reverse Escrow for “Buy NHB”)

## Intent

Enable “Buy NHB” offers where seller locks NHB and buyer locks quote asset (NHB or ZNHB) with atomic settlement when both confirm or arbitrator intervenes.

## A) Core Model (On Chain)

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
  Buyer        [20]byte
  Seller       [20]byte
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

### Atomic Settlement

- Add `SettleTradeAtomic(tradeID [32]byte)` ensuring both legs release within one state transition.
- Preconditions: both escrows funded, no unresolved disputes.
- Abort entire operation if any transfer fails.

### State & Events

- Store as `trade/<id> -> Trade`.
- Emit `escrow.trade.*` events for lifecycle stages.

### Timeouts

- If one leg funds by deadline and the other does not, refund funded leg and mark `TradeExpired`.

### Dispute/Resolve

- `TradeDisputed` when either party disputes.
- Arbitrators resolve with outcomes:
  - `release_both`
  - `refund_both`
  - `release_base_refund_quote`
  - `release_quote_refund_base`

## B) Node JSON-RPC Augmentations

- `p2p_createTrade(...) -> {tradeId, escrowBaseId, escrowQuoteId, payIntents}` creating dual escrows.
- `p2p_getTrade(tradeId)` returns trade JSON.
- `p2p_dispute`, `p2p_resolve`, `p2p_settle` orchestrate disputes and atomic release.

## C) Gateway REST Additions

```
POST /p2p/accept
Body: { "offerId":"OFF_123", "buyer":"nhb1...", "reference":"P2P-123" }
→ 201 {
  "tradeId":"0x…",
  "escrowBaseId":"0x…",
  "escrowQuoteId":"0x…",
  "payIntents": {
    "seller": { "to":"nhb1ESCROWVAULT…","token":"NHB","amount":"...","memo":"ESCROW:<escrowBaseId>","qr":"..." },
    "buyer":  { "to":"nhb1ESCROWVAULT…","token":"ZNHB|NHB","amount":"...","memo":"ESCROW:<escrowQuoteId>","qr":"..." }
  }
}
```

- `GET /p2p/trades/{tradeId}` surfaces status (`INIT|PARTIAL_FUNDED|FUNDED|DISPUTED|SETTLED|EXPIRED|CANCELLED`).
- `POST /p2p/trades/{tradeId}/settle` (mutual) requires signatures from both buyer and seller.
- `POST /p2p/trades/{tradeId}/dispute` (buyer or seller).
- `POST /p2p/trades/{tradeId}/resolve` (arbitrator) with outcome mapping.

### Expiry

- Gateway monitors deadline, auto-refunds funded leg if counterpart never funds and fires `escrow.trade.expired` webhook.

### Webhooks

- `p2p.trade.created`, `.partial_funded`, `.funded`, `.settled`, `.disputed`, `.resolved`, `.expired`, `.cancelled`.

### Security

- Same API key + HMAC.
- Wallet signatures: both parties for settle, disputing party for dispute, arbitrator for resolve.

## D) P2P Offer Semantics

- Offers specify `type` (`BUY`/`SELL`), `baseToken`, `quoteToken`, pricing, and limits.
- Acceptance computes both base and quote amounts.

## E) Loyalty Alignment

- Default: loyalty off for token-for-token P2P trades.
- Optional per-program flag `includeP2P=true` to include on release.

## F) Tests & Acceptance

### On-Chain

- Validate creation, partial funding, expiry refunds, full funding with atomic settlement, and each dispute outcome.

### Gateway

- Ensure dual pay intents, funding flows, mutual settle, expiry refunds, dispute/resolve functionality.

---

## Paste to NHBCHAIN (Delta)

```
Title: Add P2P dual-lock escrow (reverse escrow) with atomic settlement

Scope:
- Core escrow: Trade struct, atomic SettleTradeAtomic(tradeId), dispute/resolve outcomes for two-leg trades.
- Node RPC: p2p_createTrade, p2p_getTrade, p2p_settle, p2p_dispute, p2p_resolve.
- Gateway REST: POST /p2p/accept creates dual escrows & returns payIntents for buyer & seller; /p2p/trades/{id}/settle (mutual), /dispute, /resolve.
- Events: escrow.trade.* (created/funded/partial_funded/settled/disputed/resolved/expired).
- Timeouts: auto-refund funded leg if the other leg never funds by deadline.
- Security: API key + HMAC; wallet signatures (buyer/seller/arbitrator); idempotency.

Acceptance:
- go test ./... green for escrow core.
- Integration proves: Buy NHB (seller locks NHB; buyer locks ZNHB), both fund, mutual settle → atomic release; partial funding → expiry & refund; disputes resolved by arbitrator with all 4 outcome patterns.
```
