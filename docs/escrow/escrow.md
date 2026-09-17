# NHBChain Escrow & P2P Settlement

> Version: v1 (Task 5 rollout) • ChainID: **14699254016670310680** • HRPs: **nhb**, **znhb**

NHBChain's escrow module safeguards funds until a deterministic terminal outcome is reached. With Task 5 the module now drives
**atomic dual-lock settlement** for peer-to-peer (P2P) commerce, enabling both sides of a trade to fund independent legs while the
chain settles them together. This document describes the on-chain state machines, RPC interfaces, emitted events, and operational
considerations for building against the updated settlement flow.

---

## 1. Module Overview

* **Deterministic transitions.** Every state change is validated against predicates before being applied. Repeated calls on an
  escrow or trade that has already reached a terminal state are structural no-ops, so replays do not mutate state.
* **Atomic two-leg settlement.** Trades reference two escrow vaults (base and quote legs). Settlement either applies to both vaults
  or reverts entirely.
* **Dispute lifecycle.** Parties (payer/payee or buyer/seller) can dispute before final settlement. Authorized arbitrators resolve
disputes with explicit outcomes that move both escrows to terminal states.
* **Auditable history.** Transition logs, event emissions, and export APIs allow merchants to reconcile gateway activity against
chain state.

---

## 2. State Machines & Data Model

### 2.1 Escrow state machine

Each escrow record tracks token funds owned by a payer for a designated payee.

```go
// enums
const (
  EscrowInit EscrowStatus = iota
  EscrowFunding
  EscrowFunded
  EscrowReleased
  EscrowRefunded
  EscrowExpired
  EscrowDisputed
  EscrowResolved
)
```

| State             | Description                                                                                   | Terminal | Allowed transitions                                               |
|-------------------|-----------------------------------------------------------------------------------------------|----------|-------------------------------------------------------------------|
| `EscrowInit`      | Record created, no funds locked.                                                              | No       | `EscrowFunding`, `EscrowCancelled` (implicit delete)              |
| `EscrowFunding`   | Vault expects inbound transfer (watcher or module auto-collect).                              | No       | `EscrowFunded`, `EscrowExpired`                                   |
| `EscrowFunded`    | Funds held in module vault.                                                                   | No       | `EscrowReleased`, `EscrowRefunded`, `EscrowExpired`, `EscrowDisputed` |
| `EscrowReleased`  | Funds paid to payee; fee routed.                                                              | Yes      | –                                                                 |
| `EscrowRefunded`  | Funds returned to payer.                                                                      | Yes      | –                                                                 |
| `EscrowExpired`   | Deadline passed before settlement; auto-refund to payer.                                      | Yes      | –                                                                 |
| `EscrowDisputed`  | Funds frozen pending arbitrator outcome.                                                      | No       | `EscrowResolved`                                                  |
| `EscrowResolved`  | Arbitrator resolved dispute with explicit `release` or `refund`. Escrow closed with outcome.  | Yes      | –                                                                 |

> **Idempotency:** Every transition records a transition hash (`escrow/history/<id>/<seq>`) so replays of identical operations are rejected.

### 2.2 Dual-lock trade state machine

Trades orchestrate two escrow legs. Each leg is an `Escrow` (base and quote) referenced by the trade record.

```go
const (
  TradeInit TradeStatus = iota
  TradePartialFunded
  TradeFunded
  TradeDisputed
  TradeSettled
  TradeCancelled
  TradeExpired
)
```

| State                | Description                                                                 | Terminal | Allowed transitions                                                |
|----------------------|-----------------------------------------------------------------------------|----------|--------------------------------------------------------------------|
| `TradeInit`          | Trade and both escrow IDs created. No deposits observed.                    | No       | `TradePartialFunded`, `TradeCancelled`, `TradeExpired`              |
| `TradePartialFunded` | One leg funded, waiting for counterpart.                                    | No       | `TradeFunded`, `TradeExpired`, `TradeCancelled`                     |
| `TradeFunded`        | Both escrows funded. Atomic settlement or dispute can occur.                | No       | `TradeSettled`, `TradeDisputed`, `TradeExpired`                     |
| `TradeDisputed`      | Either party escalated. Both escrows frozen awaiting arbitrator decision.   | No       | `TradeSettled` (arbitrator `release` outcome), `TradeCancelled` (arbitrator `refund`) |
| `TradeSettled`       | Both escrows released atomically according to settlement outcome.           | Yes      | –                                                                  |
| `TradeCancelled`     | Both escrows refunded atomically (voluntary cancel or arbitrator outcome).  | Yes      | –                                                                  |
| `TradeExpired`       | Deadline passed before funding or settlement; both escrows refunded.        | Yes      | –                                                                  |

### 2.3 Storage & references

* `escrow/<id>` → canonical escrow struct (payer, payee, token, amount, fee, deadlines, metadata hash, status, idempotency keys).
* `escrow/bal/<id>/<token>` → amount stored in the module vault.
* `trade/<id>` → trade struct (buyer, seller, offer metadata, base/quote escrow IDs, aggregate status, dispute notes).
* `trade/history/<id>/<seq>` → canonical log of trade-level transitions.

---

## 3. Atomic Settlement Lifecycle

1. **Trade creation.** Seller publishes an offer off-chain. Buyer accepts via `p2p_createTrade` RPC (see §5). The call returns
   `tradeId`, `escrowBaseId` (seller leg), and `escrowQuoteId` (buyer leg), plus payment intents for each wallet.
2. **Funding.**
   * Each party transfers funds into their escrow vault using native token transfer or module auto-debit.
   * Watchers (gateway or merchants) call `escrow_fund` to mark completion. Once both legs are `EscrowFunded`, trade status becomes
     `TradeFunded`.
3. **Atomic settlement.**
   * When both legs are funded, either party or the gateway invokes `p2p_settle(tradeId, caller)`.
   * Settlement executes two sub-transactions inside a single commit:
     1. Release base escrow to buyer (or seller depending on offer direction) and apply fees.
     2. Release quote escrow to seller.
   * If any release fails (insufficient balance, vault transfer error), the entire transaction reverts and both escrows remain funded.
4. **Disputes.**
   * `p2p_dispute(tradeId, caller, reason)` marks both escrows as disputed. `EscrowDisputed` status prevents release/refund.
   * Arbitrators submit `p2p_resolve(tradeId, outcome, resolutionMemo)` with one of four outcomes: `release_both`, `refund_both`,
     `release_base_refund_quote`, or `release_quote_refund_base`. The decision is atomic across both escrows.
5. **Expiry & cancellation.** Deadlines are tracked at both escrow and trade level. Cancels initiated by buyer/seller unwind both
   legs.

---

## 4. Events & Monitoring

### 4.1 Escrow events

| Event                    | Emitted when                                         | Payload highlights                                         |
|--------------------------|------------------------------------------------------|-------------------------------------------------------------|
| `escrow.created`         | Escrow record created                                | `escrowId`, `payer`, `payee`, `token`, `amount`, `deadline` |
| `escrow.funded`          | Module confirms funds in vault                       | `escrowId`, `txHash`, `amount`                              |
| `escrow.released`        | Funds released to payee                              | `escrowId`, `payee`, `netAmount`, `feeAmount`               |
| `escrow.refunded`        | Funds returned to payer                              | `escrowId`, `payer`, `amount`                               |
| `escrow.expired`         | Deadline exceeded, auto-refund executed              | `escrowId`, `deadline`, `amount`                            |
| `escrow.disputed`        | Payer or payee opens dispute                         | `escrowId`, `initiator`, `reasonCode`                       |
| `escrow.resolved`        | Arbitrator settles dispute                           | `escrowId`, `outcome`, `arbitrator`, `resolutionMemo`       |

### 4.2 Trade events

| Event                          | Emitted when                                          | Payload highlights                                                           |
|--------------------------------|-------------------------------------------------------|-------------------------------------------------------------------------------|
| `escrow.trade.created`         | Trade initialized                                     | `tradeId`, `escrowBaseId`, `escrowQuoteId`, `buyer`, `seller`, `offerId`      |
| `escrow.trade.partial_funded`  | One leg funded                                        | `tradeId`, `fundedLeg`                                                        |
| `escrow.trade.funded`          | Both legs funded                                      | `tradeId`                                                                    |
| `escrow.trade.settled`         | Atomic release executed                               | `tradeId`, `releaseTxHash`, `netBase`, `netQuote`                             |
| `escrow.trade.disputed`        | Dispute opened at trade level                         | `tradeId`, `initiator`, `reasonCode`                                         |
| `escrow.trade.resolved`        | Arbitrator outcome (maps to `escrow.trade.settled`/expiry) | `tradeId`, `outcome`, `arbitrator`, `resolutionMemo`                     |
| `escrow.trade.expired`         | Deadline triggered refund                             | `tradeId`, `expiredLegs`                                                      |

Events include `sequence`, `blockHeight`, and `eventTime` fields for downstream ordering. Merchants should treat event delivery as
at-least-once and deduplicate using `eventId` + `sequence`.

---

## 5. JSON-RPC Interface

### 5.1 Escrow RPC methods

| Method | Description |
|--------|-------------|
| `escrow_create(payer, payee, token, amount, feeBps, deadline, nonce, mediator?, meta?) -> { id }` | Create escrow record; status `EscrowInit`. `nonce` is required and must be greater than 0. Optionally assign mediator and metadata. |
| `escrow_fund(id, payer)` | Marks escrow as funded after deposit. Idempotent; repeated calls ignored once funded. |
| `escrow_release(id, caller)` | Releases funds to payee. Allowed: payee, mediator, arbitrator. Fails if disputed or not funded. |
| `escrow_refund(id, caller)` | Refunds payer. Allowed: payer (pre-dispute) or arbitrator (via `escrow_resolve`). |
| `escrow_expire(id)` | Public method: if deadline passed and escrow funded but unsettled, auto-refund to payer. |
| `escrow_dispute(id, caller, reason)` | Marks escrow as disputed. Allowed: payer or payee. |
| `escrow_resolve(id, caller, outcome, memo?)` | Authorized by the escrow's own `mediator` field (caller must equal the escrow's mediator), not the global arbitrator role. Outcome `release` or `refund`. Sets `EscrowResolved` and executes atomic payout. |
| `escrow_get(id)` | Returns escrow struct, including current status, leg balances, deadlines, dispute info, and history cursor. |

All write methods require signed transactions using account keys. Idempotency is structural: repeated calls on an escrow that has
already reached a terminal state are no-ops and do not mutate state.

#### Client helper & wallet route

TypeScript integrations can rely on the `EscrowDisputeClient` helper in [`clients/ts/escrow/dispute.ts`](../../clients/ts/escrow/dispute.ts).
The helper automatically fetches the recorded payer address via `escrow_get` before invoking `escrow_dispute`, ensuring the dispute
payload uses the canonical caller. An optional reason string is forwarded for downstream audit trails:

```ts
import EscrowDisputeClient from '../../clients/ts/escrow/dispute';

const client = new EscrowDisputeClient({
  baseUrl: process.env.NHB_RPC_URL!,
  authToken: process.env.NHB_RPC_TOKEN!,
});

await client.dispute('ESC123...', 'suspected fraud');
```

Wallet surfaces can display payee identity metadata through the gateway helper route `GET /v1/consensus/wallet/escrows/{escrowId}`.
The endpoint resolves the escrow record and, when available, enriches it with the alias returned by `identity_reverse`. UI flows can
combine the gateway response with the dispute helper above to implement a “mark as scam” toggle that both freezes the escrow and
records the merchant-provided reason.

### 5.2 P2P trade RPC methods

| Method | Description |
|--------|-------------|
| `p2p_createTrade(offerId, buyer, seller, baseToken, baseAmount, quoteToken, quoteAmount, deadline, metadata?) -> { tradeId, escrowBaseId, escrowQuoteId, intents }` | Creates trade and both escrow legs. Optional metadata is hashed into each leg. |
| `p2p_getTrade(tradeId)` | Returns trade struct, aggregated status, dispute notes, escrow snapshots, and settlement history. |
| `p2p_settle(tradeId, caller)` | When both legs funded, atomically releases base to buyer and quote to seller. Caller must be buyer, seller, or gateway service key. |
| `p2p_dispute(tradeId, caller, reason)` | Moves trade to `TradeDisputed`. Both escrows become `EscrowDisputed`. |
| `p2p_resolve(tradeId, outcome, memo?, evidenceUri?)` | Arbitrator-only. Outcome must be one of `release_both`, `refund_both`, `release_base_refund_quote`, `release_quote_refund_base`. |

---

## 6. Security, Roles & Authorization

* **Atomicity guarantees.** Dual-lock settlement is guarded by a single module call which either releases both legs or reverts.
  Partial release is impossible because both `Escrow` releases share a transaction-scoped state machine lock.
* **Arbitrator role.** `p2p_resolve` is gated by `ROLE_ARBITRATOR`, with governance controlling role assignment. `escrow_resolve`
  is authorized separately: the caller must equal the escrow's own `mediator` field, not the global arbitrator role. Arbitration
  transactions must include an `arbitratorMemo` stored in history.
* **Mediator role.** Mediators can release funds (if mutually agreed off-chain) but cannot resolve disputes once flagged.
* **Deadlines & expiries.** Both escrows and trades enforce `deadline` (Unix epoch). Validators run cron-like watchers to execute
  expiry transitions; merchants should monitor events for refunds.
* **Fee routing.** Fees are debited from each escrow during release and deposited into the configured fee collector account. Fee
  configuration remains unchanged from previous releases.

---

## 7. Operational Guidelines

1. **Idempotency is structural.** `escrow_*` and `p2p_*` writes are idempotent by state: once an escrow or trade reaches a terminal
   status, repeated calls with the same parameters are no-ops and do not mutate state. There is no client-supplied idempotency key.
2. **Monitor events.** Subscribe to WebSocket or use the REST gateway webhooks (see `gateway-api.md`). Use block height + event ID
   to deduplicate.
3. **Handle disputes promptly.** Once `EscrowDisputed`, only arbitrators can resolve. Merchants should surface dispute status in
   dashboards and notify support teams.
4. **Reconciliation.** Combine on-chain events with settlement exports from the gateway (§8) to reconcile merchant balances.
5. **Testing.** Use the sandbox simulator (see `/docs/commerce/merchant-tools.md`) to exercise trade lifecycle before going live.

---

## 8. Appendices

### 8.1 Error Codes

Escrow and P2P RPC errors use standard numeric JSON-RPC error codes, not symbolic string constants. The `message` field carries a
plain Go error string describing the specific failure.

| Code     | Constant                                             | Meaning                                                                 |
|----------|-------------------------------------------------------|--------------------------------------------------------------------------|
| `-32602` | `codeInvalidParams`                                    | Malformed or missing request parameters.                                |
| `-32021` | `codeEscrowInvalidParams` / `codeP2PInvalidParams`     | Invalid parameters for an `escrow_*`/`p2p_*` call.                      |
| `-32022` | `codeEscrowNotFound` / `codeP2PNotFound`               | Escrow or trade ID not found.                                           |
| `-32023` | `codeEscrowForbidden` / `codeP2PForbidden`             | Caller lacks permission for the requested action.                       |
| `-32024` | `codeEscrowConflict` / `codeP2PConflict`               | Requested transition not valid from the escrow/trade's current status.  |
| `-32025` | `codeEscrowInternal` / `codeP2PInternal`               | Internal error while processing the request.                            |
| `-32010` | `codeDuplicateTx`                                      | Duplicate transaction (same sender/nonce) already known.                |
| `-32030` | `codeMempoolFull`                                      | Mempool full; resubmit later.                                           |

### 8.2 Reference types

```go
type Escrow struct {
  ID         [32]byte
  Payer      Address
  Payee      Address
  Mediator   *Address
  Token      string
  Amount     *big.Int
  FeeBps     uint32
  Deadline   int64
  CreatedAt  int64
  MetaHash   [32]byte
  Status     EscrowStatus
  Dispute    *DisputeInfo // null unless disputed
  HistoryPos uint64       // last history sequence number
}

type Trade struct {
  ID            [32]byte
  OfferID       string
  Buyer         Address
  Seller        Address
  BaseEscrowID  [32]byte
  QuoteEscrowID [32]byte
  Deadline      int64
  CreatedAt     int64
  Status        TradeStatus
  LastActionAt  int64
  Dispute       *TradeDisputeInfo
}
```

Use these structures as canonical references when building merchant integrations.
