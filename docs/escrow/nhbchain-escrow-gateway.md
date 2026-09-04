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
GATEWAY_CHAIN_ID=14699254016670310680
GATEWAY_API_RATE_RPS_PER_KEY=5
GATEWAY_HMAC_ALG=HMAC_SHA256
GATEWAY_WEBHOOK_MAX_RETRY=10
```

The names above are illustrative of the original design; `LoadConfigFromEnv`
(`services/escrow-gateway/config.go`) is the source of truth for what's
actually read, and uses an `ESCROW_GATEWAY_` prefix throughout (e.g.
`ESCROW_GATEWAY_NODE_URL`, `ESCROW_GATEWAY_API_KEYS`). One variable is new
as of the meta-transaction rewrite above and **required** for the service to
start: `ESCROW_GATEWAY_RELAYER_KMS_ENV` names another environment variable
holding the gateway's own raw hex-encoded secp256k1 private key (same
indirection pattern as `services/payments-gateway`'s
`PAY_GATEWAY_ATTESTOR_KMS_ENV`) — this is the relayer key that signs and
pays gas for every delegated transaction the gateway submits (see "Node
Integration" below). It must hold enough NHB to cover gas for expected
traffic.

### Authentication & Authorization

- API key + HMAC per developer/app (e.g., “usedtown”), on every request.
- **Create, release, refund, and dispute additionally require a participant wallet
  signature** — as of the meta-transaction rewrite below, **create** now needs one
  too, not just release/dispute/resolve as in the original design: the escrow's
  `payer` is only ever set to whoever actually signed the create request, never
  trusted from the API-key-authenticated caller's say-so alone.
  - `X-Sig-Addr`: `nhb…`/`znhb…` — the participant's own address (payer for
    create/refund, payee or mediator for release, payer or payee for dispute).
  - `X-Sig`: `hex(ecdsa_sign(keccak256(canonical_json_envelope)))` — **not**
    EIP-191-wrapped, and **not** a signature over the HTTP request (method/path/
    body/timestamp/nonce) the way earlier drafts of this doc described. The
    participant signs the exact on-chain authorization envelope the gateway will
    relay in a real transaction:
    - Create: `{"action":"create","payer":"<20-byte hex>","payee":"<20-byte hex>","token":"NHB|ZNHB","amount":"<decimal>","feeBps":<uint>,"deadline":<unix>,"nonce":<uint>,"mediator":"<hex, omitted if unset>","meta":"<hex, omitted if unset>","realm":"<id, omitted if unset>"}` — note `payer`/`payee`/`mediator` are lower-case hex of the raw 20 address bytes here, not the `nhb1…` bech32 string used everywhere else in this API; the gateway performs that conversion server-side before verifying.
    - Release/Refund: `{"escrowId":"0x<64 hex>","action":"release"}` (or `"refund"`).
    - Dispute: `{"escrowId":"0x<64 hex>","action":"dispute","reason":"<free text, omitted if empty>"}`.
  - `X-Timestamp`/`X-Nonce`: still required for the outer HMAC-signed request
    (API-key layer, replay protection on the REST transport itself), but no
    longer part of what the wallet signature covers — the wallet signature's
    own replay safety comes from the on-chain action being idempotent once
    submitted (a captured signature can be resubmitted with zero additional
    effect), not from a timestamp/nonce baked into the signed bytes.
- The gateway verifies the signature server-side (fast, clear 403 on mismatch)
  *and* the chain re-verifies it independently once the gateway relays the
  action in a real transaction — the gateway's own key never authorizes
  anything by itself, it only pays gas and owns that transaction's nonce. See
  "Node Integration" below for why this changed.
- **Resolve is not available yet.** The safe on-chain resolve path requires the
  escrow to have been created against a registered arbitration realm (a
  committee of arbitrators + signing threshold), and no deployment has
  provisioned one yet — that's a one-time operator action, not something the
  gateway does per-request. `POST /escrow/resolve` currently returns `503`.

### Production Deployment

Live since 2026-09-04, co-located with validator1 on `52.1.96.250`:

- Binary: `/opt/nhbchain/bin/escrow-gateway`, systemd unit `escrow-gateway.service`, env file `/etc/nhbchain/escrow-gateway.env` (holds `ESCROW_GATEWAY_RELAYER_KMS_ENV` and friends — see Configuration above).
- Public routing: nginx on `api.nhbcoin.com` proxies `/escrow/` and `/p2p/` to `localhost:8081`. No separate `gateway.` or `escrow.` subdomain exists or is planned — `api.nhbcoin.com` is the one public API host for both the chain's own JSON-RPC and this gateway's REST surface.
- Relayer address: a dedicated NHB account funded specifically to pay gas for every delegated transaction this service submits (see "Node Integration" below) — it does not custody user funds, only pays its own transaction fees. **This balance needs periodic monitoring/top-up**; there is no automated low-balance alert wired up yet.
- Quota: every delegated transaction this gateway submits lands on the recovered *transaction signer* (the relayer's own address, not the end user) for `native/common`'s per-sender request-rate quota (`moduleEscrow`, `config.toml`'s `[global.Quotas.Escrow]`, currently a generous `6000`/min) — worth knowing if the gateway is ever scaled to run genuinely high volume through a single relayer key, since all of that traffic shares one quota bucket.

### Idempotency

- `Idempotency-Key` header captured with `{appKey, route, key}` for all write endpoints.

### Funding Model (NHB/ZNHB Only)

1. Funds held by on-chain vault (E1).
2. Gateway only creates on-chain escrow via node RPC and returns pay intent (address + memo/ID) plus optional QR.
3. Gateway watches chain events to update REST status & trigger webhooks.

### REST Endpoints

```
POST /escrow/create
Headers: API key, HMAC, X-Sig-Addr (payer), X-Sig (see Authentication above), Idempotency-Key
Body: {
  "payer":   "nhb1...",
  "payee":   "nhb1...",
  "token":   "NHB" | "ZNHB",
  "amount":  "100000000000000000000",
  "feeBps":  0,
  "deadline": 1730000000,
  "mediator": "nhb1..." | null,
  "meta":     "0x<hex, optional>"
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
Headers: API key, HMAC, X-Sig-Addr (payee or mediator), X-Sig, X-Timestamp, X-Nonce, Idempotency-Key
Body: { "escrowId": "0x…" }
→ 202 { "queued": true }
```

```
POST /escrow/refund
Headers: API key, HMAC, X-Sig-Addr (payer), X-Sig, X-Timestamp, X-Nonce, Idempotency-Key
Body: { "escrowId": "0x…" }
→ 202 { "queued": true }
```

```
POST /escrow/dispute
Headers: API key, HMAC, X-Sig-Addr (payer or payee), X-Sig, X-Timestamp, X-Nonce, Idempotency-Key
Body: { "escrowId":"0x…", "reason":"item damaged" }
→ 202 { "ok": true }
```

```
POST /escrow/resolve
Headers: API key, HMAC, Idempotency-Key
Body: { "escrowId":"0x…", "outcome":"release"|"refund" }
→ 503 -- not yet available, see Authentication above.
```

### Webhooks

- `escrow.created`, `escrow.funded`, `escrow.released`, `escrow.refunded`, `escrow.expired`, `escrow.disputed`, `escrow.resolved`.
- Payload: `{escrowId, payer, payee, token, amount, txHash?, meta.reference, provider}` with `provider` covering the realm scope/type, provider profile, arbitration fee basis points, and fee recipient address.

### Node Integration

- **`escrow_create`/`release`/`refund`/`dispute`/`resolve` are permanently
  disabled chain-side** (see `rpc/escrow_handlers.go`'s
  `escrowRPCDisabledMessage`) — they used to mutate validator state directly
  outside the block pipeline, which guaranteed a consensus fork on this
  2-validator chain the moment any of them was ever called. The gateway no
  longer calls them at all.
- Mutating actions now go through real signed transactions
  (`TxTypeDelegatedCreateEscrow`/`ReleaseEscrow`/`RefundEscrow`/
  `DisputeEscrow`, `core/types/transaction.go`), submitted via
  `nhb_sendTransaction` and signed with the gateway's own relayer key
  (`ESCROW_GATEWAY_RELAYER_KMS_ENV`) — the relayer pays gas and owns each
  transaction's nonce, but authorization for the underlying action comes
  entirely from the participant's signature embedded in the transaction
  payload (verified independently on-chain, not just by this gateway). See
  `native/escrow/engine.go`'s `*WithSignature` methods and
  `core/state_transition.go`'s `applyDelegated*Escrow` for the chain-side
  verification this relies on.
- `escrow_get`/`escrow_getRealm` remain live, read-only RPC calls — nothing
  about reads changed.
- Subscribes to block/events to sync status.

### Security & Abuse Mitigation

- Per-key rate limits; HMAC body integrity.
- Wallet signature proves payer/payee/mediator control over the specific
  on-chain action being authorized, verified both by this gateway (fast,
  clear 403) and independently by the chain itself once relayed.
- Idempotency on writes.
- Append-only audit log (request hash, actor, escrowId, node RPC result, block/tx).

### P2P Trade Creation — Retired

`POST /p2p/accept`'s underlying trade-creation RPC (`p2p_createTrade`) is
permanently disabled for the same guaranteed-fork reason as the escrow RPCs
above, and — unlike escrow create/release/refund/dispute — has no
signed-transaction replacement, because the bilateral OTC trade flow it
fronted is superseded by the P2P ZNHB market (`native/market`, live since
2026-08-24), which already does atomic seller-escrows/buyer-pays swaps
through real signed transactions. `POST /p2p/accept` now always returns a
502 with a clear "permanently retired" message. `POST /p2p/offers`,
`GET /p2p/offers`, and `GET /p2p/trades/{id}` (which don't call the disabled
RPC) are unaffected.

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

- Events: none are emitted for offer creation or acceptance — `handleCreateOffer`/`handleAcceptOffer` only perform database writes.
- Integration: seller creates offer → buyer accepts → funds → release → seller receives.

## Part C — Escrow Pay Intent Specification

- **Vault**: module vault bech32 per token.
- **Memo/Data**: `ESCROW:<idhex>` or ABI call `depositEscrow(bytes32 id)`.
- **QR URI**: `znhb://pay?to=<vault>&token=<NHB|ZNHB>&amount=<wei>&memo=ESCROW:<idhex>`.

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

- `GET /p2p/trades/{tradeId}` surfaces status (`INIT|PARTIAL_FUNDED|FUNDED|DISPUTED|SETTLED|EXPIRED|CANCELLED`). This is a
  read-only lookup — there is no gateway REST endpoint to settle, dispute, or resolve a trade. Those actions go through the
  node's `p2p_settle`, `p2p_dispute`, and `p2p_resolve` JSON-RPC methods directly (see Part B above).

### Expiry

- Gateway monitors deadline, auto-refunds funded leg if counterpart never funds and fires `escrow.trade.expired` webhook.

### Webhooks

- `escrow.trade.created`, `.partial_funded`, `.funded`, `.disputed`, `.resolved`, `.settled`, `.expired` (the gateway's trade
  watcher switches on the on-chain `escrow.trade.*` namespace, not `p2p.trade.*`). There is no `.cancelled` variant.

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
- Gateway REST: POST /p2p/accept creates dual escrows & returns payIntents for buyer & seller; GET /p2p/trades/{id} for status (read-only — settle/dispute/resolve go through node RPC, not REST).
- Events: escrow.trade.* (created/funded/partial_funded/settled/disputed/resolved/expired).
- Timeouts: auto-refund funded leg if the other leg never funds by deadline.
- Security: API key + HMAC; wallet signatures (buyer/seller/arbitrator); idempotency.

Acceptance:
- go test ./... green for escrow core.
- Integration proves: Buy NHB (seller locks NHB; buyer locks ZNHB), both fund, mutual settle → atomic release; partial funding → expiry & refund; disputes resolved by arbitrator with all 4 outcome patterns.
```
