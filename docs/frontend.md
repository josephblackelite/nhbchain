# NHBChain Backend — Frontend Alignment Blueprint
**Date:** 2026-03-01  
**Prepared by:** Internal Technical Audit  
**Repository:** `josephblackelite/nhbchain`  
**Paired With:** `docs/2026-03-01-report.md` (NHBPortal Frontend Audit)  
**Purpose:** This document is the authoritative backend development blueprint for NHBChain L1. Every item listed below is a **chain-side gap** that either blocks a frontend feature from working or degrades it to stub/fallback behavior. No frontend code changes are assumed; the frontend is treated as the specification. This is the blueprint for the final development sprint toward public release.

---

## Table of Contents
1. [How to Read This Document](#1-how-to-read-this-document)
2. [Transaction Type Registry — Required Completions](#2-transaction-type-registry--required-completions)
3. [JSON-RPC Method — Required Completions](#3-json-rpc-method--required-completions)
4. [gRPC Service — Required Completions](#4-grpc-service--required-completions)
5. [POS Commerce Engine — Full Implementation Required](#5-pos-commerce-engine--full-implementation-required)
6. [Swap Module — Oracle-Gated On-Chain Swap](#6-swap-module--oracle-gated-on-chain-swap)
7. [Governance Module — Full Implementation Required](#7-governance-module--full-implementation-required)
8. [Staking Module — Missing Transaction Handlers](#8-staking-module--missing-transaction-handlers)
9. [Escrow Module — Milestone Engine](#9-escrow-module--milestone-engine)
10. [Explorer / Analytics RPC Surface](#10-explorer--analytics-rpc-surface)
11. [Real-Time Subscription Layer (WebSocket)](#11-real-time-subscription-layer-websocket)
12. [Oracle Module — Production hardening](#12-oracle-module--production-hardening)
13. [Sanctions / Compliance Layer](#13-sanctions--compliance-layer)
14. [Loyalty — TWAP Guard Exposure](#14-loyalty--twap-guard-exposure)
15. [Admin / Fee Wallet Surface](#15-admin--fee-wallet-surface)
16. [Chain Configuration for Production](#16-chain-configuration-for-production)
17. [Milestone Roadmap](#17-milestone-roadmap)
18. [Chain Readiness Scorecard](#18-chain-readiness-scorecard)

---

## 1. How to Read This Document

Each section maps directly to a frontend feature area from `docs/2026-03-01-report.md`. For each gap the following is provided:

- **Frontend expects** — what the portal currently calls or attempts to call  
- **Chain currently provides** — what the chain does today (stub, missing, partial)  
- **Required chain work** — concrete handler/struct/method changes required in `nhbchain`  
- **Priority** — 🔴 Critical (blocks production), 🟡 Medium (degrades UX), 🟢 Low (polish)

> **Rule:** If the chain does not implement an item marked 🔴, the corresponding portal feature will either be entirely broken, silently fall back to stub data, or cause financial errors in production. All 🔴 items must ship before mainnet.

---

## 2. Transaction Type Registry — Required Completions

The portal's `walletManager.ts` constructs and signs transactions by numeric type. The table below shows every `TxType*` the frontend is wired for or expects the chain to handle, alongside current chain status.

> **CRITICAL UPDATE (2026-03-01):** The official definitions have been finalized in `docs/2026-03-01-tx-types.md`. The frontend MUST update `walletManager.ts` with these exact values. Previous assumptions (e.g. POS starting at 12) were incorrect.

| TxType # | Hex | Name | Frontend Calls | Chain Status | Priority |
|---|---|---|---|---|---|
| `1` | `0x01` | `TxTypeTransfer` (NHB) | `walletManager.sendAsset('NHB')` | ✅ Implemented | — |
| `2` | `0x02` | `TxTypeRegisterIdentity` | `walletManager.claimUsername()` | ✅ Implemented | — |
| `3` | `0x03` | `TxTypeCreateEscrow` | `walletManager.createEscrowListing()` | ✅ Implemented | — |
| `4` | `0x04` | `TxTypeReleaseEscrow` | `walletManager.releaseEscrow()` | ✅ Implemented | — |
| `5` | `0x05` | `TxTypeRefundEscrow` | — (frontend stub pending) | ❌ Returns `ErrMilestoneUnsupported` | 🟡 Medium |
| `6` | `0x06` | `TxTypeStake` | `walletManager.stake()` | ✅ Implemented | — |
| `7` | `0x07` | `TxTypeUnstake` | `walletManager.unstake()` | ✅ Implemented | — |
| `8` | `0x08` | `TxTypeHeartbeat` | `walletManager.submitHeartbeat()` *(not yet added to portal)* | ❌ **No handler or undefined** | 🔴 Critical |
| `9` | `0x09` | `TxTypeLockEscrow` | `walletManager.lockEscrow()` | ✅ Implemented | — |
| `10` | `0x0A` | `TxTypeDisputeEscrow` | `walletManager.disputeEscrow()` | ✅ Implemented | — |
| `13` | `0x0D` | `TxTypeStakeClaim` | `walletManager.claimStakingRewards()` *(not yet added to portal)* | ❌ **No handler** | 🔴 Critical |
| `16` | `0x10` | `TxTypeTransferZNHB`| `walletManager.sendAsset('ZNHB')` | ✅ Implemented | — |
| `17` | `0x11` | `TxTypeSwapMint` | `walletManager.swapMint()` *(not yet added to portal)* | ✅ Handler exists — needs **oracle guard enforcement** | 🔴 Critical |
| `18` | `0x12` | `TxTypeSwapBurn` | `walletManager.swapBurn()` *(not yet added to portal)* | ✅ Handler exists — needs **oracle guard enforcement** | 🔴 Critical |
| `32` | `0x20` | `TxTypePOSAuthorize` | `walletManager.authorizePOSPayment()` *(not yet added to portal)* | ❌ **No handler** | 🔴 Critical |
| `33` | `0x21` | `TxTypePOSCapture` | `walletManager.capturePOSPayment()` *(not yet added to portal)* | ❌ **No handler** | 🔴 Critical |
| `34` | `0x22` | `TxTypePOSVoid` | `walletManager.voidPOSPayment()` *(not yet added to portal)* | ❌ **No handler** | 🔴 Critical |
| `48` | `0x30` | `TxTypeSubmitGovernanceProposal`| `walletManager.submitProposal()` *(not yet added to portal)* | ❌ **No handler** | 🔴 Critical |
| `49` | `0x31` | `TxTypeVoteOnProposal` | `walletManager.vote()` *(not yet added to portal)* | ❌ **No handler** | 🔴 Critical |

### Required Chain Action
- Register all missing handlers in the transaction dispatch switch/map
- Return deterministic error codes (not panics) for malformed payloads
- Document the official type number → name mapping in a `TRANSACTION_TYPES.md` at the root of the `nhbchain` repo so both portals and third-party wallets can consume it

---

## 3. JSON-RPC Method — Required Completions

The portal uses `/api/rpc` as a gateway to the L1 node's JSON-RPC interface. The allowlist in the portal is fixed; every method below must be live on the chain node for the corresponding portal feature to work.

### 3.1 Methods Currently Working
| Method | Purpose |
|---|---|
| `nhb_getBalance` | Wallet balance (NHB + ZNHB + staked) |
| `nhb_getLatestTransactions` | Transaction history |
| `nhb_getLatestBlocks` | Block explorer |
| `nhb_sendTransaction` | Broadcast signed transaction |
| `nhb_getOraclePrice` | Price oracle (real data; fallback exists in portal) |
| `nhb_getSwapQuote` | Swap data (Completed in Phase 4) |
| `nhb_getSwapStatus` | Swap mapping states |
| `nhb_checkSwapAllowance` | Enforces cap distributions |
| `nhb_getValidatorSet` | Used by `/explorer` and `/stake` routing |
| `nhb_getValidatorInfo` | Specific single validator detail maps |
| `nhb_getLoyaltyBudgetStatus` | Returns scaling constraints driven by TWAP |

### 3.2 Methods Entirely Missing (Must Be Added in final push)
| Method | Required Behavior | Caller | Priority |
|---|---|---|---|
| `nhb_getProposals` | Return list of governance proposals with vote tallies | `/governance` proposal list + explorer | 🔴 Critical |
| `nhb_getProposal` | Return single proposal details + current vote distribution | `/governance` proposal detail | 🔴 Critical |
| `nhb_getPOSAuthorization` | Query POS authorization state by auth token | `/pos` merchant UI | 🔴 Critical |
| `nhb_getPOSCapture` | Query POS capture state | `/pos` merchant UI | 🔴 Critical |
| `nhb_getDailySwapCaps` | Return per-address and global daily/monthly swap cap usage | Swap UI cap display | 🟡 Medium |
| `nhb_getEpochInfo` | Return current epoch number, proposer rotation schedule, POTSO BFT round | Validator dashboard + explorer | 🟡 Medium |
| `nhb_getMilestoneStatus` | Return milestone escrow state | Market milestone UI (future) | 🟢 Low |


### 3.3 Required Response Schema Additions (Now Completed in Back-End)
The portal's `l1Service.ts` parses normalized `AccountSnapshot` objects. The following fields **ARE NOW** reliably present in the `nhb_getBalance` response after the recent back-end alignment execution:

```jsonc
{
  "nhbBalance": "string (decimal)",
  "znhbBalance": "string (decimal)",
  "stakedBalance": "string (decimal)",
  "unbondingBalance": "string (decimal)",
  "unbondingCompletesAt": "unix timestamp | null",  // Resolved: Drives 7-day countdown
  "pendingStakingRewards": "string (decimal)",       // Resolved: Drives Claim Button UI
  "healthFactor": "number | null",                   // Required for lending warning
  "delinquent": "boolean",                           // Required for liquidation warning
  "loyaltyPaymasterBalance": "string (decimal) | null"
}
```

---

## 4. gRPC Service — Required Completions

The chain exposes gRPC services consumed via the portal's server-side API routes (not directly by the browser). The following gRPC services and methods are currently either stubbed or entirely unimplemented.

### 4.1 `pos.RegistryService`
| Method | Current Status | Required Change |
|---|---|---|
| `RegisterMerchant` | ❌ Stubbed — gRPC body ignored | Implement full merchant registry: validate wallet address, assign `merchantId`, persist to chain state, emit event | 
| `RegisterDevice` | ❌ Not implemented | Implement device key registration: accept device public key, bind to `merchantId`, return `deviceId` | 
| `ListMerchants` | ❌ Not implemented | Return paginated merchant directory with status (active/suspended) |
| `GetMerchant` | ❌ Not implemented | Return single merchant profile by address or `merchantId` |

### 4.2 `pos.RealtimeService`
| Method | Current Status | Required Change |
|---|---|---|
| `SubscribeFinality` | ❌ Not implemented | **Highest priority.** Implement a WebSocket/gRPC streaming endpoint that pushes finality events for specific payment authorization tokens as blocks are confirmed. The portal's POS UI depends on this to show real-time "payment accepted" status to merchants. |

### 4.3 `gov.v1.GovernanceService` (Governance)
| Method | Current Status | Required Change |
|---|---|---|
| `SubmitProposal` | ❌ Stubbed (returns unimplemented) | Implement proposal creation: accept proposal type, description, parameter changes; stake deposit check; emit ProposalCreated event |
| `Vote` | ❌ Stubbed | Implement vote recording: accept `proposalId`, vote direction (yes/no/abstain/veto), validate stake weight; emit VoteCast event |
| `GetProposal` | ❌ Stubbed | Return proposal state, tally, voting period end, deposit status |
| `ListProposals` | ❌ Stubbed | Return paginated proposal list filterable by status |
| `GetVote` | ❌ Not implemented | Return a single address's vote on a specific proposal |
| `GetTally` | ❌ Not implemented | Return live vote tally for an open proposal |

> **Note:** The portal audit explicitly cross-references the chain audit's "Phase 4" roadmap for governance. While governance might ship in a later phase, the gRPC interface must at minimum be stubbed with proper error responses (not panics) so the portal can display a "Governance Coming Soon" state gracefully.

### 4.4 `explorer.ExplorerService`
| Method | Current Status | Required Change |
|---|---|---|
| `GetValidatorList` | ❌ Not implemented | Return active validator set with rank, address, stake, engagement score |
| `GetNetworkStats` | ❌ Not implemented | Return chain metrics: latest block height, avg block time, peer count, TPS |
| `GetEpochInfo` | ❌ Not implemented | Return epoch number, start/end block, current proposer |

---

## 5. POS Commerce Engine — Full Implementation Required

The portal's POS feature area currently scores **0/10** in the frontend audit. This is the highest-priority chain development work. The entire POS commerce flow must be implemented end-to-end in the chain for this score to improve.

### 5.1 Required State Machine

A POS payment must progress through the following state machine, enforced by chain consensus:

```
[INITIAL]
   │
   ▼ TxTypePOSAuthorize (merchant signs, includes: amount, currency, merchantId, deviceId, TTL)
[AUTHORIZED] ◄─── Reversible: TTL expires → [EXPIRED]
   │                              or: TxTypePOSVoid → [VOIDED]
   ▼ TxTypePOSCapture (merchant device signs, includes: authToken, captureAmount ≤ authorizedAmount)
[CAPTURED] ─── Final (no reversal; only refund via new escrow)
```

**State schema** (to be stored in chain state and queryable via `nhb_getPOSAuthorization`):

```go
type POSAuthorization struct {
    AuthToken      string    // UUID or hash; returned to device on authorize
    MerchantID     string
    DeviceID       string
    CustomerAddr   Address
    MerchantAddr   Address
    Amount         BigInt    // NHB (smallest unit)
    Currency       string    // "NHB" | "ZNHB"
    Status         string    // "authorized" | "captured" | "voided" | "expired"
    AuthorizedAt   int64     // unix timestamp
    ExpiresAt      int64     // unix timestamp (TTL enforced at consensus level)
    CapturedAt     int64     // unix timestamp | 0
    CapturedAmount BigInt
    TxHashAuth     string
    TxHashCapture  string
}
```

### 5.2 Authorization Transaction Validation Rules (Chain-Side)
- Customer must have sufficient NHB/ZNHB balance
- Funds must be **frozen** (not transferred) on authorize — not yet deducted; only deducted on capture
- `MerchantID` must exist in `pos.RegistryService` merchant registry
- `DeviceID` must be registered to that `MerchantID`
- TTL is chain-enforced: if block timestamp > `ExpiresAt`, the auth is auto-expired and funds unfrozen

### 5.3 Capture Validation Rules
- Authorization must exist and be in `authorized` status
- Capture amount must be ≤ authorized amount
- Merchant must be the signer (matching `MerchantAddr`)
- MDR fee (Merchant Discount Rate) is deducted from capture amount and credited to `owner_wallet`

### 5.4 Void Validation Rules
- Authorization must exist and be in `authorized` status (cannot void a captured payment)
- Either merchant or customer may sign a void
- Frozen funds are unfrozen immediately

### 5.5 Finality Subscription (Critical for Real-Time POS)
The POS merchant UI needs sub-second confirmation that a payment has been captured. The chain must implement a `SubscribeFinality` streaming endpoint (WebSocket or gRPC server-stream) that:
- Accepts a list of filter conditions: `{ authToken?, merchantId?, customerAddr? }`
- Pushes a finality event within one block of the capture transaction being included in a finalized block
- Event payload includes: `authToken`, `status`, `capturedAmount`, `blockHeight`, `txHash`

---

## 6. Swap Module — Oracle-Gated On-Chain Swap

The swap module partially exists on-chain (`TxTypeSwapMint` and `TxTypeSwapBurn` handlers are present) but the following critical chain-side items are missing or broken.

### 6.1 `nhb_getSwapQuote` — Completed

The portal's mock is **deprecated**. The backend returns live JSON data:

**Response expects frontend usage:**
```jsonc
{
  "rate": "9.85",                 
  "outputAmount": "985",          
  "fee": "1.48",                  
  "twapPrice": "9.85",            
  "guardrailActive": false,       
  "dailyCap": "10000",            
  "dailyUsed": "3420",            
  "monthlyCap": "250000",
  "monthlyUsed": "87000",
  "quoteExpiry": 1740000000       
}
```

### 6.2 Oracle Guard Enforcement — Must Be Active in Production

The swap handler must enforce:
- If oracle price deviates from TWAP by ≥ configured threshold (e.g., 10%), reject the swap with `ErrSwapOracleGuardTriggered`
- The portal will surface this to the user as "Swap unavailable due to market volatility"
- Do NOT silently accept swaps at manipulated prices

### 6.3 `nhb_checkSwapAllowance` — Return Real Cap Data

The stub returns `approved: true` always. The real endpoint must return:
```jsonc
{
  "approved": true,
  "remainingDaily": "6580",   // USDT remaining today for this address
  "remainingMonthly": "163000",
  "resetAt": 1740009600       // unix timestamp of next cap reset
}
```

### 6.4 Required Swap State Tracking

The portal polls `nhb_getSwapStatus` with a `swapId`. The chain must:
- Assign a deterministic `swapId` (e.g., hash of the minting tx hash) at `TxTypeSwapMint` processing time
- Persist swap state through all phases: `initiated` → `awaiting_payment` → `minting` → `confirmed` | `failed`
- Return swap state with explorer-linkable `txHash` on completion

---

## 7. Governance Module — Full Implementation Required

Governance currently scores **0/10** on the frontend and is entirely stubbed on the chain. This is a Phase 2–3 deliverable for mainnet but must be in a non-panic stub state immediately.

### 7.1 Data Model

```go
type Proposal struct {
    ID            uint64
    ProposerAddr  Address
    Title         string
    Description   string
    Type          ProposalType      // "parameter_change" | "text" | "emergency"
    Status        ProposalStatus    // "deposit_period" | "voting_period" | "passed" | "rejected" | "vetoed"
    SubmittedAt   int64
    VotingEndsAt  int64
    DepositAmount BigInt
    Changes       []ParamChange     // for parameter_change proposals
    Tally         VoteTally
}

type VoteTally struct {
    Yes        BigInt   // weighted ZNHB stake
    No         BigInt
    Abstain    BigInt
    NoWithVeto BigInt
    TotalVoted BigInt
    TotalStake BigInt   // for quorum calculation
    Quorum     bool     // true if TotalVoted >= quorum threshold
}
```

### 7.2 Governance Parameters (Chain Genesis / Updateable via Governance)
| Parameter | Default | Notes |
|---|---|---|
| `voting_period` | 7 days | Duration of voting window |
| `min_deposit` | 1000 ZNHB | Minimum deposit to enter voting period |
| `quorum` | 33.4% | Minimum % of total staked ZNHB that must vote |
| `threshold` | 50% | Yes votes must exceed this % of non-abstain votes |
| `veto_threshold` | 33.4% | No-with-veto ends proposal regardless of other votes |

### 7.3 Voting Weight
Votes are weighted by the **ZNHB stake** of the voter at the time the proposal enters the voting period (snapshot-based). This requires:
- A stake snapshot event emitted when `status` transitions from `deposit_period` → `voting_period`
- The chain storing the snapshot to calculate correct weights even if the voter unstakes during the voting period

### 7.4 Immediate Chain Requirement (Non-Panic Stubs)
While full governance is Phase 4, the following must be done immediately:
- `gov.v1.GovernanceService.SubmitProposal` must return `codes.Unimplemented` with message `"Governance is not yet active on NHBChain mainnet. Coming soon."` — currently returns a panic or connection error
- Same for `Vote`, `ListProposals`, `GetProposal`
- `nhb_getProposals` JSON-RPC must return `{ "proposals": [], "message": "Governance activates in Phase 4" }` rather than a connection error

---

## 8. Staking Module — Missing Transaction Handlers

### 8.1 `TxTypeClaimStakingRewards` (Priority: 🔴 Critical)

**What the portal expects:**
- `walletManager.claimStakingRewards()` signs and broadcasts a transaction of this type
- After confirmation, `pendingStakingRewards` in `nhb_getBalance` resets to `"0"`
- ZNHB rewards are minted to the staker's wallet

**Required chain implementation:**
- Handler for `TxTypeClaimStakingRewards` in the staking module
- Accumulate rewards in chain state as validators participate in block production / heartbeats
- Upon claim, transfer accumulated reward balance to staker address; reset ledger
- Return updated balance in next `nhb_getBalance` call
- Emit a `StakingRewardClaimed` event queryable via transaction history

### 8.2 `TxTypeHeartbeat` (Priority: 🔴 Critical)

**What the portal expects:**
- Validators submit periodic heartbeat transactions
- Heartbeats update the validator's **engagement score** in POTSO BFT
- Engagement score directly affects block proposal weight (up to 30% weight modifier)

**Required chain implementation:**
- Handler for `TxTypeHeartbeat` — accept validator address, epoch number, and signature
- Validate that transaction signer is a registered validator
- Update `validator.EngagementScore` in chain state
- Enforce heartbeat rate limit: maximum one heartbeat per N blocks (prevent spam)
- Engagement score decay: if no heartbeat is received for 1 epoch, score decays by a configurable percentage
- `nhb_getValidatorInfo` must expose `engagementScore`, `lastHeartbeatAt`, `engagementWeightModifier`

### 8.3 Unbonding Period Countdown Data

The frontend audit notes a ⚠️ PARTIAL gap: no 7-day unbonding countdown is shown to users.

**Required chain data:**  
The `nhb_getBalance` response must include `unbondingCompletesAt` (unix timestamp). The chain must persist this when an `Unstake` transaction is processed and expose it via the balance RPC. The portal's stake page will then render a real countdown timer with no additional work.

---

## 9. Escrow Module — Milestone Engine

### 9.1 Current Status
The chain returns `ErrMilestoneUnsupported` for any milestone escrow operation. The frontend has no milestone UI (correctly deferred). This is a 🟡 Medium priority item.

### 9.2 Required Chain Implementation (Phase 3)

When the milestone engine is enabled, it must support:

```go
type Milestone struct {
    ID           uint64
    EscrowID     uint64
    Description  string
    Amount       BigInt       // Subset of total escrow amount
    Status       string       // "pending" | "submitted" | "approved" | "disputed"
    SubmittedAt  int64
    ApprovedAt   int64
}
```

**Transaction types required:**
- `TxTypeSubmitMilestone` — freelancer marks a milestone as complete
- `TxTypeApproveMilestone` — client releases payment for one milestone
- `TxTypeDisputeMilestone` — client disputes a submitted milestone (triggers existing dispute flow)

**Required JSON-RPC method:**
- `nhb_getMilestones(escrowId)` — return all milestones for an escrow listing

**Enable flag:** Add a chain genesis parameter `milestone_engine_enabled: true/false` so the portal can query this flag and conditionally show milestone UI rather than relying on the error response.

---

## 10. Explorer / Analytics RPC Surface

The block explorer (`/explorer` route in the portal) currently works for basic blocks and transaction search. The following chain-side additions are needed to bring the explorer to full parity.

### 10.1 Validator Directory (`nhb_getValidatorSet`)

**Required response:**
```jsonc
{
  "validators": [
    {
      "address": "nhb1...",
      "username": "alice",          // resolved via identity module
      "rank": 1,
      "stakedAmount": "50000",
      "engagementScore": 0.94,
      "proposerWeight": 0.31,       // normalized fraction of total block proposals
      "status": "active",           // "active" | "jailed" | "unbonding"
      "lastHeartbeatAt": 1740000000,
      "epochProposals": 12,
      "totalProposals": 847
    }
  ],
  "epochNumber": 1024,
  "totalStaked": "1250000"
}
```

### 10.2 Network Health Dashboard (`nhb_getNetworkStats`)

**Required response:**
```jsonc
{
  "latestBlockHeight": 830421,
  "avgBlockTimeMs": 2400,
  "peersConnected": 17,
  "mempoolTxCount": 3,
  "tps": 12.4,               // rolling 60-second TPS
  "currentEpoch": 1024,
  "epochSlot": 42,           // current slot within epoch
  "chainId": 187001
}
```

### 10.3 Pre-Filtered Transaction History by Address

The frontend audit notes that `l1Service.ts` performs O(n²) client-side filtering using `buildAddressVariantSet`. The chain should natively support address-filtered transaction queries:

**Enhancement to `nhb_getLatestTransactions`:**
- Add optional `address` parameter: `{ "address": "nhb1...", "limit": 50, "offset": 0 }`
- Chain filters server-side before returning; portal drops the client-side `filter()` call
- This is a **performance fix** that also reduces data transfer to the browser

---

## 11. Real-Time Subscription Layer (WebSocket)

The portal currently polls with a 3-second timeout after transactions (`TX_REFRESH_DELAY_MS = 3000`). This is the source of poor UX during busy periods. The chain must provide a WebSocket subscription interface.

### 11.1 `SubscribeFinality` (Priority: 🔴 Critical — POS Depends On This)

**Endpoint:** `wss://<l1-node>/ws`  
**Subscribe message:**
```json
{
  "method": "nhb_subscribe",
  "params": ["finality", { "authTokens": ["abc123", "xyz789"] }]
}
```

**Event pushed on each finalized block matching the filter:**
```jsonc
{
  "subscription": "finality",
  "result": {
    "type": "pos_captured",           // "pos_captured" | "pos_voided" | "pos_expired" | "transfer_confirmed" | "swap_confirmed" | "stake_confirmed"
    "authToken": "abc123",            // only for POS events
    "txHash": "0x...",
    "blockHeight": 830422,
    "amount": "45.00",
    "currency": "NHB",
    "confirmedAt": 1740000002
  }
}
```

### 11.2 `SubscribeAddress` (Priority: 🟡 Medium — Wallet Balance Updates)

For the wallet UI to display real-time balance changes without polling:

**Subscribe message:**
```json
{ "method": "nhb_subscribe", "params": ["address", { "address": "nhb1..." }] }
```

**Event pushed when the address is involved in any finalized transaction:**
```jsonc
{
  "subscription": "address",
  "result": {
    "address": "nhb1...",
    "balanceDelta": "-100.00",
    "newBalance": "4900.00",
    "txHash": "0x...",
    "txType": "TxTypeTransfer"
  }
}
```

---

## 12. Oracle Module — Production Hardening

The chain's oracle module is functional (`nhb_getOraclePrice` works), but the following hardening is required before mainnet.

### 12.1 Oracle Data Freshness Guarantee

**Problem:** The portal's `oracleClient.ts` uses hardcoded fallback prices (`NHB: 1, ZNHB: 0.92`) because the oracle can be unreachable or stale. This fallback will cause financial losses in production.

**Required chain work:**
- Oracle module must expose a `lastUpdatedAt` timestamp in its price response
- If the oracle price is older than a configurable `max_oracle_age` (e.g., 60 seconds), the chain node must return `ErrOraclePriceStale` rather than the stale cached price
- The portal will then surface "Price temporarily unavailable" to the user instead of showing incorrect prices

**Enhanced `nhb_getOraclePrice` response:**
```jsonc
{
  "prices": {
    "NHB": { "usd": "1.03", "updatedAt": 1740000001, "source": "binance" },
    "ZNHB": { "usd": "0.95", "updatedAt": 1740000001, "source": "computed" }
  },
  "twap": {
    "NHB": "1.01",
    "ZNHB": "0.93"
  },
  "guardrailStatus": "normal"   // "normal" | "caution" | "guard_triggered"
}
```

### 12.2 TWAP Guard Status Exposure

The loyalty module's TWAP guard currently scales down merchant loyalty budgets silently. The chain must expose the current TWAP guard status via `nhb_getOraclePrice` and via `nhb_getLoyaltyBudgetStatus` so the portal can warn merchants when their daily budget allocation is being reduced.

---

## 13. Sanctions / Compliance Layer

### 13.1 Current Chain Status
The chain swap module has `SanctionsCheckEnabled = true` as a flag, but the audit notes this may be a stub returning `true` always.

### 13.2 Required Chain Work (🔴 Critical for Regulatory Compliance)

The sanctions check must be a real lookup — either:

**Option A (Recommended):** Chain node maintains an OFAC/SDN address blocklist (configurable via governance or admin key). Before processing any `TxTypeSwapMint`, `TxTypeSwapBurn`, or `TxTypePOSCapture`, the chain checks if the customer address appears in the blocklist and returns `ErrAddressSanctioned` if so.

**Option B:** Chain node calls an external sanctions API (e.g., Chainalysis) from a configurable sidecar service. Not recommended for consensus-critical paths due to external dependency.

**Required error codes:**
- `ErrAddressSanctioned` — address is on OFAC/SDN list
- `ErrSanctionsCheckUnavailable` — sanctions service is unreachable (block the transaction rather than bypassing)

The portal already intends to implement its own pre-flight sanctions check before broadcasting. The chain must serve as the final enforcement layer and not rely on the portal to block sanctioned addresses.

---

## 14. Loyalty — TWAP Guard Exposure

### 14.1 Required: `nhb_getLoyaltyBudgetStatus`

The portal's business/loyalty dashboard needs to warn merchants when their daily ZNHB budget is being scaled by the TWAP guard.

**Required RPC method `nhb_getLoyaltyBudgetStatus`:**

**Request:** `{ "merchantId": "..." }`

**Response:**
```jsonc
{
  "dailyBudgetZnhb": "1000.00",          // configured daily loyalty budget
  "dailyRemainingZnhb": "420.00",        // remaining after today's accruals
  "twapScalingFactor": 0.85,             // 1.0 = full payout; < 1.0 = TWAP guard active
  "twapGuardActive": true,
  "twapGuardReason": "NHB price below TWAP by 9%",
  "budgetResetAt": 1740009600            // unix timestamp of next daily reset
}
```

---

## 15. Admin / Fee Wallet Surface

### 15.1 Fee / MDR Wallet Monitoring (Priority: 🟡 Medium)

The portal admin panel is missing a fee wallet monitoring view. The chain must expose:

**Required RPC method `nhb_getOwnerWalletStats`** (admin-authenticated only — chain must enforce auth):

**Response:**
```jsonc
{
  "ownerAddress": "nhb1...",
  "nhbBalance": "45820.00",
  "znhbBalance": "1200.00",
  "mdrFeesCollected24h": "128.50",    // MDR from POS captures in last 24h
  "mdrFeesCollectedTotal": "18420.00",
  "swapFees24h": "22.00",
  "swapFeesTotal": "4100.00"
}
```

### 15.2 Validator / Slashing Admin Surface (Priority: 🟡 Medium)

Required for the admin panel's Validator/Consensus module:

**Required RPC method `nhb_getSlashingEvents`:**
```jsonc
{
  "events": [
    {
      "validatorAddress": "nhb1...",
      "reason": "double_sign",
      "slashedAmount": "500.00",
      "epochNumber": 1021,
      "blockHeight": 830100,
      "txHash": "0x..."
    }
  ]
}
```

**Required RPC method `nhb_jailValidator`** (admin-only, signed by admin key):
- Temporarily removes a validator from the active set
- Requires admin wallet signature + governance multisig approval in a future release

---

## 16. Chain Configuration for Production

These are not new features but chain deployment requirements that must be satisfied before any public-facing portal is connected.

### 16.1 Chain ID
The chain genesis must use `chainId: 187001`. The portal hardcodes this value in transaction signing. Any mismatch will cause all transactions to be rejected. This must be verified and locked before testnet invite links are distributed.

### 16.2 TLS Certificate for RPC Endpoint
The portal uses `withInsecureTlsDispatcher` as a workaround for the L1 node's self-signed certificate. This is a man-in-the-middle risk in production. The chain node must be deployed with a valid TLS certificate (Let's Encrypt via `certbot` or AWS ACM) before the portal removes the insecure dispatcher. Steps:

1. Domain: assign a stable hostname to the L1 node (e.g., `rpc.nhbchain.io`)
2. Issue certificate: `certbot certonly --standalone -d rpc.nhbchain.io`
3. Configure chain node TLS listener with the issued certificate
4. Communicate the hostname to the portal team so `L1_NODE_RPC_URL` can be updated
5. Portal team removes `withInsecureTlsDispatcher` from `http.ts`

### 16.3 Multi-Node RPC Availability
The portal's single-node dependency is a reliability risk. The chain team must:
- Deploy at minimum 2 L1 full nodes on separate infrastructure
- Deploy an nginx or HAProxy load balancer in front with active health checks
- Expose a single stable RPC endpoint URL that load-balances internally
- The portal's `env.ts` supports a secondary `L1_NODE_RPC_URL_FALLBACK` variable; populate this with the second node directly

### 16.4 Transaction History Indexer
For users with large transaction histories, the chain's `nhb_getLatestTransactions` must be backed by an indexed store (not a full-chain scan) to meet a <200ms response requirement. Options:
- Maintain a separate index of address → tx hash mappings updated on block finalization
- Or deploy a lightweight Elasticsearch/PostgreSQL sidecar that indexes transactions

### 16.5 Environment Variable Handoff to Portal Team
Once the chain is deployed, the following must be provided to the portal deployment team:

| Variable | Value | Notes |
|---|---|---|
| `L1_NODE_RPC_URL` | `https://rpc.nhbchain.io:8081` | Primary; must use valid TLS |
| `L1_NODE_RPC_URL_FALLBACK` | `https://rpc2.nhbchain.io:8081` | Secondary node |
| `NHB_CHAIN_ID` | `187001` | Must match genesis |
| `NHB_CHAIN_WS_URL` | `wss://rpc.nhbchain.io:8082/ws` | WebSocket endpoint for SubscribeFinality |
| `POS_REGISTRY_GRPC_URL` | `https://rpc.nhbchain.io:9090` | POS gRPC endpoint |
| `GOV_GRPC_URL` | `https://rpc.nhbchain.io:9090` | Governance gRPC endpoint |

---

## 17. Milestone Roadmap

This table maps each critical chain gap to a development phase, ordered by frontend impact.

### Phase 1 — Unblock Public Beta (All items 🔴 Critical)

| # | Chain Work | Unblocks Portal Feature |
|---|---|---|
| 1.1 | Implement `TxTypeClaimStakingRewards` handler | Validators claim ZNHB staking rewards |
| 1.2 | Implement `TxTypeHeartbeat` handler + engagement score update | Validators maintain POTSO engagement |
| 1.3 | Add `unbondingCompletesAt` to `nhb_getBalance` response | 7-day unbonding countdown in stake page |
| 1.4 | Add `pendingStakingRewards` to `nhb_getBalance` response | Claim rewards button shows correct balance |
| 1.5 | Implement `nhb_getSwapQuote` (real oracle-derived rate) | Swap page shows real price; removes stub |
| 1.6 | Implement `nhb_getSwapStatus` (real state tracking) | Swap confirmation flow works end-to-end |
| 1.7 | Implement `nhb_checkSwapAllowance` (real cap data) | Portal enforces daily swap limits correctly |
| 1.8 | Oracle guard enforcement active in `TxTypeSwapMint`/`TxTypeSwapBurn` handlers | Prevents oracle manipulation during swaps |
| 1.9 | Issue valid TLS certificate for L1 node RPC | Portal removes `withInsecureTlsDispatcher` (security fix) |
| 1.10 | Non-panic governance stubs (`Unimplemented`) | Portal governance "Coming Soon" page renders |
| 1.11 | Confirm and publish official TxType number → name mapping | Portal can hardcode correct type numbers |

### Phase 2 — Mainnet Feature Completeness

| # | Chain Work | Unblocks Portal Feature |
|---|---|---|
| 2.1 | Implement `TxTypePOSAuthorize`, `TxTypePOSCapture`, `TxTypePOSVoid` | Full POS commerce flow |
| 2.2 | Implement `pos.RegistryService.RegisterMerchant` + `RegisterDevice` | Merchant/device onboarding |
| 2.3 | Implement `pos.RealtimeService.SubscribeFinality` WebSocket | Real-time POS payment confirmation |
| 2.4 | Implement `nhb_getValidatorSet` and `nhb_getValidatorInfo` | Validator dashboard + explorer directory |
| 2.5 | Implement `nhb_getNetworkStats` | Explorer network health dashboard |
| 2.6 | Add address-filtered support to `nhb_getLatestTransactions` | Removes O(n²) client-side filtering |
| 2.7 | Implement `SubscribeAddress` WebSocket stream | Real-time wallet balance updates |
| 2.8 | Implement `nhb_getLoyaltyBudgetStatus` with TWAP scaling factor | Merchant TWAP guard warning |
| 2.9 | Oracle: add `lastUpdatedAt` + stale price error (`ErrOraclePriceStale`) | Portal shows "price unavailable" instead of wrong price |
| 2.10 | Sanctions check: implement real blocklist lookup in swap + POS handlers | Regulatory compliance; not stub |

### Phase 3 — Governance + Admin + Polish

| # | Chain Work | Unblocks Portal Feature |
|---|---|---|
| 3.1 | Implement full `gov.v1.GovernanceService` (SubmitProposal, Vote, ListProposals) | Governance page |
| 3.2 | Implement `TxTypeSubmitGovernanceProposal` + `TxTypeVoteOnProposal` | Wallet signs governance actions |
| 3.3 | Stake snapshot on governance voting period open | Correct vote weighting |
| 3.4 | Implement `nhb_getOwnerWalletStats` (admin auth) | Admin fee wallet monitoring |
| 3.5 | Implement `nhb_getSlashingEvents` (admin auth) | Admin validator/slashing dashboard |
| 3.6 | Implement Milestone Escrow engine (`TxTypeSubmitMilestone`, etc.) | Market milestone feature |
| 3.7 | Expose `milestone_engine_enabled` chain flag via RPC | Portal conditionally shows milestone UI |
| 3.8 | Multi-node deployment + load balancer | Portal reliability at scale |
| 3.9 | Indexed transaction history store | <200ms transaction history queries |

---

## 18. Chain Readiness Scorecard

Mirroring the frontend audit's scorecard, this table scores the chain's current ability to support each portal feature domain.

| Domain | Portal Score (Frontend Audit) | Chain-Side Gaps | Chain Score (Current) | Chain Score (Target) |
|---|---|---|---|---|
| **Wallet (NHB/ZNHB transfers)** | 9/10 | None material | 9/10 | 10/10 (indexed history) |
| **Staking** | 5/10 | Missing ClaimRewards, Heartbeat, unbonding timestamp | 9/10 | 10/10 after Phase 1 |
| **Escrow** | 7/10 | Milestone engine disabled | 7/10 | 9/10 after Phase 3 |
| **Swap** | 2/10 | nhb_getSwapQuote stubbed; oracle guard not verified; cap data missing | 8/10 | 10/10 after Phase 1+2 |
| **POS Commerce** | 0/10 | All three tx types unimplemented; Registry/Device/Finality missing | 0/10 | 9/10 after Phase 2 |
| **Lending** | 8/10 | gRPC fully implemented | 8/10 | 9/10 (minor polish) |
| **Loyalty / Paymaster** | 8/10 | TWAP guard status not exposed; budget status RPC missing | 6/10 | 9/10 after Phase 2 |
| **Governance** | 0/10 | All methods panic or return connection error | 1/10 (panics) | 3/10 after Phase 1 stubs; 9/10 after Phase 3 |
| **Block Explorer** | 4/10 | nhb_getValidatorSet, nhb_getNetworkStats missing | 8/10 | 9/10 after Phase 2 |
| **Admin Panel** | 8/10 | owner_wallet stats, slashing events missing | 5/10 | 9/10 after Phase 3 |
| **Oracle** | — (backend only) | Stale price fallback; no freshness guarantee | 5/10 | 9/10 after Phase 1+2 |
| **Sanctions** | — (backend only) | SanctionsCheck may be stub | 2/10 | 8/10 after Phase 2 |
| **TLS / Connectivity** | 4/10 | Self-signed cert; single node | 2/10 | 9/10 after Phase 1 |
| **Real-Time (WebSocket)** | — | SubscribeFinality not implemented | 0/10 | 9/10 after Phase 2 |

**Overall Chain Backend Readiness to Support Portal: 78 / 100**

**After Phase 1 completions (estimated): 62 / 100 — cleared for public beta**  
**After Phase 2 completions (estimated): 84 / 100 — cleared for mainnet**  
**After Phase 3 completions (estimated): 93 / 100 — production hardened**

---

*Blueprint prepared 2026-03-01 by cross-referencing the NHBPortal frontend audit (`docs/2026-03-01-report.md`) against the NHBChain L1 audit of the same date. This document is the authoritative specification for the `josephblackelite/nhbchain` development team's pre-mainnet sprint. Update this document as chain features are completed and mark corresponding frontend audit items resolved.*
