# NHBChain Escrow & P2P Settlement

> Version: v1 (Task 5 rollout) • ChainID: **187001** • HRPs: **nhb**, **znhb**

NHBChain's escrow module safeguards funds until a deterministic terminal outcome is reached. With Task 5 the module now drives
**atomic dual-lock settlement** for peer-to-peer (P2P) commerce, enabling both sides of a trade to fund independent legs while the
chain settles them together. This document describes the on-chain state machines, RPC interfaces, emitted events, and operational
considerations for building against the updated settlement flow.

---

## 1. Module Overview

* **Deterministic transitions.** Every state change is validated against predicates and idempotency keys stored on-chain. Replay or
  duplicate submissions are rejected.
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
   * Arbitrators submit `p2p_resolve(tradeId, outcome, resolutionMemo)` with an outcome of `release` (buyer receives base asset,
     seller receives quote) or `refund` (both parties refunded). The decision is atomic across both escrows.
5. **Expiry & cancellation.** Deadlines are tracked at both escrow and trade level. If deadline passes before funding, watchers
   call `p2p_expire(tradeId)` which refunds whichever legs are funded. Cancels initiated by buyer/seller also unwind both legs.

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

| Event                   | Emitted when                                          | Payload highlights                                                           |
|-------------------------|-------------------------------------------------------|-------------------------------------------------------------------------------|
| `trade.created`         | Trade initialized                                     | `tradeId`, `escrowBaseId`, `escrowQuoteId`, `buyer`, `seller`, `offerId`      |
| `trade.partialFunded`   | One leg funded                                        | `tradeId`, `fundedLeg`                                                        |
| `trade.funded`          | Both legs funded                                      | `tradeId`                                                                    |
| `trade.settled`         | Atomic release executed                               | `tradeId`, `releaseTxHash`, `netBase`, `netQuote`                             |
| `trade.disputed`        | Dispute opened at trade level                         | `tradeId`, `initiator`, `reasonCode`                                         |
| `trade.resolved`        | Arbitrator outcome (maps to `trade.settled`/`cancel`) | `tradeId`, `outcome`, `arbitrator`, `resolutionMemo`                          |
| `trade.expired`         | Deadline triggered refund                             | `tradeId`, `expiredLegs`                                                      |
| `trade.cancelled`       | Cancel executed prior to settlement                   | `tradeId`, `cancelledBy`                                                      |

Events include `sequence`, `blockHeight`, and `eventTime` fields for downstream ordering. Merchants should treat event delivery as
at-least-once and deduplicate using `eventId` + `sequence`.

---

## 5. JSON-RPC Interface

### 5.1 Escrow RPC methods

| Method | Description |
|--------|-------------|
| `escrow_create(payer, payee, token, amount, feeBps, deadline, mediator?, metaHash?) -> { id }` | Create escrow record; status `EscrowInit`. Optionally assign mediator and metadata hash. |
| `escrow_fund(id, payer)` | Marks escrow as funded after deposit. Idempotent; repeated calls ignored once funded. |
| `escrow_release(id, caller)` | Releases funds to payee. Allowed: payee, mediator, arbitrator. Fails if disputed or not funded. |
| `escrow_refund(id, caller)` | Refunds payer. Allowed: payer (pre-dispute) or arbitrator (via `escrow_resolve`). |
| `escrow_expire(id)` | Public method: if deadline passed and escrow funded but unsettled, auto-refund to payer. |
| `escrow_dispute(id, caller, reason)` | Marks escrow as disputed. Allowed: payer or payee. |
| `escrow_resolve(id, caller, outcome, memo?)` | Arbitrator-only. Outcome `release` or `refund`. Sets `EscrowResolved` and executes atomic payout. |
| `escrow_get(id)` | Returns escrow struct, including current status, leg balances, deadlines, dispute info, and history cursor. |

All write methods require signed transactions using account keys. Transactions include a per-call `idempotencyKey` stored on-chain to
prevent replay.

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
| `p2p_resolve(tradeId, outcome, memo?, evidenceUri?)` | Arbitrator-only. Outcome `release` (trade settles) or `refund` (trade cancels). |
| `p2p_expire(tradeId)` | Public method. Refunds any funded legs after deadline. |
| `p2p_listTrades(filter)` | Returns paginated list (mainly for light clients; heavy listing should use REST gateway). |

All RPC responses include `commitHash` (block hash) so clients can anchor snapshots.

---

## 6. Security, Roles & Authorization

* **Atomicity guarantees.** Dual-lock settlement is guarded by a single module call which either releases both legs or reverts.
  Partial release is impossible because both `Escrow` releases share a transaction-scoped state machine lock.
* **Arbitrator role.** Addresses with `ROLE_ARBITRATOR` can call `escrow_resolve` and `p2p_resolve`. Governance controls role
  assignment. Arbitration transactions must include an `arbitratorMemo` stored in history.
* **Mediator role.** Mediators can release funds (if mutually agreed off-chain) but cannot resolve disputes once flagged.
* **Deadlines & expiries.** Both escrows and trades enforce `deadline` (Unix epoch). Validators run cron-like watchers to execute
  expiry transitions; merchants should monitor events for refunds.
* **Fee routing.** Fees are debited from each escrow during release and deposited into the configured fee collector account. Fee
  configuration remains unchanged from previous releases.

---

## 7. Operational Guidelines

1. **Use idempotency keys.** Pass stable UUIDs when invoking `escrow_*` and `p2p_*` writes. Replays with same key and payload result
   in `ERR_DUPLICATE_REQUEST` and do not mutate state.
2. **Monitor events.** Subscribe to WebSocket or use the REST gateway webhooks (see `gateway-api.md`). Use block height + event ID
   to deduplicate.
3. **Handle disputes promptly.** Once `EscrowDisputed`, only arbitrators can resolve. Merchants should surface dispute status in
   dashboards and notify support teams.
4. **Reconciliation.** Combine on-chain events with settlement exports from the gateway (§8) to reconcile merchant balances.
5. **Testing.** Use the sandbox simulator (see `/docs/commerce/merchant-tools.md`) to exercise trade lifecycle before going live.

---

## 8. Appendices

### 8.1 Error Codes

| Code                    | Meaning                                                                                     |
|-------------------------|---------------------------------------------------------------------------------------------|
| `ERR_INVALID_STATUS`    | Requested transition not allowed from current status.                                      |
| `ERR_DEADLINE_EXCEEDED` | Operation attempted after deadline.                                                         |
| `ERR_NOT_AUTHORIZED`    | Caller lacks permission (e.g., non-arbitrator attempted resolve).                           |
| `ERR_ATOMIC_ABORT`      | Atomic trade settlement failed due to vault transfer error; no state change occurred.       |
| `ERR_DUPLICATE_REQUEST` | Idempotency key replay detected; request ignored.                                           |
| `ERR_CONFLICTING_FUND`  | Funding attempt mismatched expected token/amount.                                           |

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
