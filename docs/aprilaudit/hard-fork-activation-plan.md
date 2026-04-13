# NHBChain Hard Fork Activation Plan

Date: 2026-04-11
Scope: live-network upgrade planning for the repaired NHBChain protocol, with rollout sequencing, operator coordination, wallet and SDK alignment, and post-fork product phases.

## Executive Summary

NHBChain is now in a materially stronger technical state after the repair pass. The codebase is green, the core commerce engine is tighter, and the most dangerous payment-layer flaws are closed in-repo.

However, because the network is already online, some of the most important fixes are consensus or signing-semantics changes. They must be activated as a coordinated hard fork, not as an informal rolling patch.

The hard fork should be used for three things only:

1. protect transaction integrity
2. restore exact settlement accounting
3. remove hidden or unsafe transfer behavior

Everything else should be layered on top of that stable base in controlled phases.

## What The Hard Fork Must Cover

These are the minimum fork-scope items:

### 1. Transaction signing-domain correction
- bind `IntentRef` into the signed native transaction hash
- preserve `IntentExpiry` binding
- make the payment intent domain explicit and stable going forward

Why it matters:
- POS and payment intent references become cryptographically meaningful
- a signed payment can no longer be redirected to the wrong business reference without invalidating the signature

### 2. NHB transfer gas-accounting correction
- enforce one correct settlement rule:
  - sender pays `value + gas` when unsponsored
  - paymaster pays gas when sponsorship is valid
- ensure event output and ledger movement reconcile exactly

Why it matters:
- NHB transfers cannot be the base payment rail if debits and validation disagree
- wallets, merchant ledgers, and accounting systems need exact settlement truth

### 3. Removal of hidden transfer-routing fee behavior
- eliminate any silent or hardcoded routing tax from transfer execution
- if fees exist, they must be explicit, policy-driven, and visible to clients

Why it matters:
- hidden deductions destroy trust in a financial rail
- merchant and consumer accounting must always explain every debit and credit

## What Should Not Be Mixed Into The Fork

Do not overload the hard fork with broad feature additions.

The fork should not directly introduce:
- a brand new wallet architecture
- broad lending redesign
- treasury re-platforming
- new banking connectors
- major SDK rewrites
- frontend-only UX changes

Those belong in post-fork phases.

## Activation Principles

The activation program should follow these rules:

### 1. One source of economic truth
- no hidden fees
- no undocumented routing deductions
- no mismatch between actual credited value and emitted value

### 2. Version every protocol-facing change
- new node release version
- explicit fork height
- explicit RPC/client compatibility note
- explicit wallet/SDK minimum versions

### 3. Keep merchant operations uninterrupted
- POS authorization/capture should remain the preferred merchant rail during transition
- direct transfer semantics must be clearly versioned for clients

### 4. Wallets must fail safe
- pre-fork wallets that do not support the new signing domain must not silently send invalid or partially compatible transactions

## Required Deliverables Before Mainnet Activation

The following must exist before a live fork date is finalized:

### Protocol
- release candidate node binary
- exact fork-height logic
- transaction hash/signing version notes
- replay and mixed-version test results

### Operators
- validator upgrade guide
- archive/full-node upgrade guide
- RPC operator checklist
- rollback and incident playbook

### Wallet / SDK / Gateway
- updated SDK release for transaction construction and signing rules
- wallet compatibility matrix for `nhbportal`
- RPC compatibility note for integrators
- merchant integrator bulletin describing the new semantics

### Governance / Business
- signed founder-level fee policy decision
- official statement on whether the free tier is:
  - monthly
  - rolling window
  - lifetime threshold
- explicit rule for when gas starts charging
- explicit treasury recipient for paid gas

## Recommended Rollout Sequence

## Phase 0. Governance And Policy Lock

Purpose:
- freeze the commercial rules before operators upgrade

Decisions to lock:
- fork name
- fork block height
- supported client version floor
- exact fee policy after fork
- explicit free-tier policy
- explicit treasury/admin wallet receiving paid gas

Output:
- signed activation memo
- final protocol change set
- public operator notice

## Phase 1. Release Candidate Build

Purpose:
- prepare the exact binary and RPC behavior to be promoted

Tasks:
- cut a release branch for the fork
- tag the repaired node release
- lock dependencies
- publish release notes with:
  - hard-fork scope
  - breaking changes
  - wallet impact
  - operator action required

Output:
- `nhbchain` release candidate
- deterministic build artifact set

## Phase 2. Shadow Network / Testnet Validation

Purpose:
- prove the fork behavior before mainnet coordination

Tests required:
- replay of recent mainnet-like transactions
- direct NHB transfer before/after semantics
- sponsored NHB transfers
- POS payment intent settlement
- escrow create/fund/release/refund/dispute
- swap quote/reserve/cashout/abort
- lending transactions and accounting sanity
- governance proposals and timelock execution
- heartbeat / POTSO / rewards continuity

Key validation questions:
- do all debits and credits reconcile?
- do old wallets fail cleanly?
- do new wallets sign and submit correctly?
- do explorer/event feeds reflect actual delivered values?

Output:
- fork certification report

## Phase 3. Wallet And SDK Synchronization

Purpose:
- ensure no wallet or integrator sends stale-format transactions into a live fork

Tasks:
- publish updated SDK for:
  - transaction signing
  - intent reference handling
  - error codes
  - fee interpretation
- update `nhbportal` to:
  - use the upgraded SDK
  - reject stale chain behavior assumptions
  - surface fees clearly when threshold is crossed
- update API examples and merchant integration snippets

Output:
- wallet release candidate
- SDK release notes
- merchant migration bulletin

## Phase 4. Validator And Infrastructure Coordination

Purpose:
- prepare the network for a single coordinated cutover

Participants:
- validators
- RPC operators
- explorers
- merchant gateways
- swap / treasury services
- indexers

Tasks:
- distribute exact binary hash
- confirm operator readiness
- require acknowledgement from validator set
- require staging upgrade confirmation from RPC endpoints
- freeze nonessential config changes before fork

Output:
- operator readiness registry

## Phase 5. Mainnet Fork Activation

Purpose:
- execute the cutover at the agreed height

Execution steps:
- monitor approach to fork height
- verify validator majority is upgraded
- watch first post-fork block production
- verify no chain split
- verify transaction acceptance on upgraded RPC nodes
- verify wallets submit valid post-fork transfers

Immediate post-fork checks:
- NHB transfer test
- sponsored NHB transfer test
- POS-tagged payment test
- escrow lifecycle test
- swap reserve/cashout test
- governance read/write sanity

Output:
- live activation confirmation

## Phase 6. Post-Fork Stabilization Window

Purpose:
- treat the first 24 to 72 hours as a controlled observation period

Monitor:
- block production continuity
- finality times
- failed transaction rate
- sponsorship rejection rate
- RPC error rate
- explorer/event correctness
- treasury fee inflow correctness
- merchant payment success rate

Hold during window:
- no extra protocol changes
- no unrelated parameter changes
- no aggressive product launches

Output:
- post-fork health report

## Fee-Free Transfer Vision: Recommended Safe Design

The founder vision should not be implemented as a hidden tax or undocumented special case.

The correct design is:

### Free Tier Model
- every eligible user has a tracked free-transfer or free-payment allowance
- the allowance is policy-based and auditable
- while allowance remains:
  - gas is sponsored
  - sender sees fee = 0
- once exhausted:
  - gas is charged normally
  - charged gas is routed explicitly to the genesis treasury/admin wallet

### Required Policy Choices

NHBChain should formally choose one:

1. monthly free tier up to `1000 NHB`
2. rolling 30-day free tier up to `1000 NHB`
3. lifetime onboarding subsidy up to `1000 NHB`

Recommended:
- monthly free tier

Reason:
- aligns with the original marketing better
- is easier for users and merchants to understand
- behaves like a financial product plan, not a one-time promotion

### Rules That Must Be Explicit
- what transactions count toward the threshold
- whether sponsored POS counts
- whether self-transfers count
- whether refunds restore allowance
- whether the threshold is per user, per wallet, or per verified identity

## Financial-Rail Operating Model After The Fork

NHBChain should operate as five clearly separated rails.

### 1. Retail Payment Rail
- NHB payments
- POS authorization/capture/void
- refunds
- merchant receipts
- realtime settlement confirmations

### 2. Settlement Rail
- merchant treasury movement
- business payouts
- batched settlements
- reconciliation exports

### 3. Mint / Redeem Rail
- USDT/USDC in
- NHB mint
- NHB burn / redeem
- payout receipt generation
- treasury inventory checks

### 4. Escrow / Marketplace Rail
- milestone escrow
- disputes
- mediated resolution
- marketplace settlement

### 5. Financial Services Rail
- lending
- savings/yield products
- governance-controlled service modules
- institutional rails

This separation will make NHBChain easier to operate, govern, audit, and sell.

## Lending Review In The Activation Context

The lending module should not block the hard fork, but it should enter the next delivery phase immediately after fork stabilization.

Recommended lending actions:
- full market parameter review
- exact collateral and liquidation accounting audit
- treasury fee and reserve routing review
- lender/borrower event reconciliation review
- formal service exposure plan for banks and institutional partners

Target product framing:
- NHBChain lending should become a native credit and liquidity service layer for regulated or partner-driven institutions, not just a speculative DeFi add-on

## `nhbportal` Coordination Requirements

The wallet application is now a critical dependency for safe rollout.

`nhbportal` must be updated to:
- consume the new SDK
- support the corrected signing domain
- display transfer fee state correctly
- distinguish sponsored vs paid gas
- surface merchant receipts and payment references clearly

High-priority wallet policy note:
- private-key regeneration from email plus device-derived hashing should not be treated as the final security architecture for a financial network

Recommended direction:
- passkey-backed encrypted recovery
- MPC / split-key recovery
- account-abstraction style guardians

## Merchant / Integrator Communication Plan

Before activation, publish a short integrator note covering:

- exact fork height
- minimum SDK version
- whether transaction hash semantics changed
- how fee-free behavior works after fork
- what merchants should expect in receipts and events
- what to do if pre-fork clients are still in use

This matters because NHBChain is positioning itself as a Stripe/Visa-class rail, and merchants care most about:
- uptime
- settlement finality
- clear receipts
- deterministic fees
- simple integration

## Post-Fork Delivery Roadmap

After the fork stabilizes, the next roadmap should be:

## Phase A. Transparent Free-Tier Engine
- implement explicit sponsored usage ledger
- tie paid gas to treasury wallet
- publish user-visible threshold logic

## Phase B. Treasury Control Plane
- hot-wallet and cold-wallet orchestration
- redemption workflow approvals
- treasury reconciliation dashboards
- provider failover controls

## Phase C. Wallet Security Upgrade
- `nhbportal` SDK integration
- stronger recovery model
- merchant-grade receipt UX

## Phase D. Merchant Platform Package
- stable SDKs
- webhook guarantees
- settlement exports
- dispute/escrow console
- merchant reporting APIs

## Phase E. Banking And Institutional Layer
- lending exposure for banks
- treasury accounts
- compliance integrations
- payout and settlement policy surfaces

## No-Go Conditions

Do not activate the fork if any of these are unresolved:

- wallet or SDK still signs stale-format transactions
- validator majority is not confirmed upgraded
- post-fork replay tests are incomplete
- transfer accounting still differs from emitted results
- fee policy is not formally decided
- treasury destination for paid gas is not fixed and verified
- explorer/indexer stack is not fork-aware

## Final Position

NHBChain now has a viable path to become the commerce-grade financial rail described in the founder vision.

What it is now:
- a broad, real, multi-rail blockchain commerce platform with repaired core transaction safety

What it can become after disciplined rollout:
- a stable-value payment and settlement network combining:
  - merchant simplicity
  - realtime finality
  - native escrow
  - native rewards
  - institutional lending and treasury rails
  - API-first integration for global commerce

The right next move is not more uncontrolled feature expansion.

The right next move is:
- activate the repaired protocol safely
- formalize the fee and treasury model
- synchronize the wallet and SDK
- then scale the financial services surface on top of a trusted base
