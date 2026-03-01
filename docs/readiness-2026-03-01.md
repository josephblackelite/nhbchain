# NHBChain Mainnet Readiness & Technical Blueprint (V1)
**Date:** 2026-03-01  
**Status:** Mainnet Ready  
**Audience:** Frontend Developers, Stakeholders, Investors, Ecosystem Partners  

---

## 1. Executive Summary

NHBChain is officially mainnet-ready. It is a purpose-built Layer 1 payment rail and loyalty network designed for real-world commerce. By integrating identity, compliance, and zero-fee transactions natively into the protocol, NHBChain eliminates the complexities of traditional crypto (seed phrases, gas confusion, hex addresses) and replaces them with a seamless, traditional finance-grade user experience.

This document serves as the master technical blueprint and feature exhaustive reference for developers building on NHBChain (like the NHBPortal frontend) and stakeholders evaluating the network's capabilities.

---

## 2. Core Technical Specifications

*   **Consensus Engine:** Proof of Time Spent Online (POTSO)
*   **Virtual Machine:** EVM Compatible (Go-Ethereum Engine)
*   **Primary Assets:** 
    *   `NHB` (Dollar-pegged stablecoin for commerce)
    *   `ZNHB` (ZapNHB - Loyalty, staking, and governance asset)
*   **Block Time:** ~3 Seconds
*   **Transaction Fees:** Sponsored / Zero-fee for end-users via native account abstraction.
*   **Max Transactions Per Block:** 500 (Configurable via Governance)
*   **RPC Interface:** JSON-RPC 2.0 (HTTP & WebSocket)

---

## 3. Human-Readable Transacting (Identity Native)

The core goal of NHBChain is to deprecate the usage of raw hexadecimal wallet addresses (e.g., `0x123...`) in favor of universal, human-readable identifiers. 

**Frontend Developers Must Implement:**
*   **Email & Username Transfers:** The chain natively supports resolving identities. Users can send funds directly to an `@username` or a verified email address. 
*   **Account Abstraction:** Wallets should be generated securely via Social Login/Email without exposing seed phrases to the user.
*   **Identity Endpoints:** Utilize `nhb_identitySetAlias`, `nhb_identitySetPrimary`, and `nhb_identityAddAddress` to map complex EVM addresses to simple identifiers.

---

## 4. Connecting and Developing on NHBChain

### 4.1 RPC and WebSocket Interfaces
Frontend clients interact with the chain via the primary JSON-RPC multiplexer.
*   **Standard Queries:** `nhb_getBalance`, `nhb_getTransaction`, `nhb_getEpochSnapshot`.
*   **Real-time Commerce:** Connect to the `SubscribeFinality` WebSocket (`/ws/pos/finality`) for instant payment confirmations at point-of-sale terminals.
*   **Rate Limiting & Security:** Public RPCs enforce strictly managed quotas to prevent spam, while merchant endpoints authenticate via HMAC keys (`RPCSwapAuth`).

### 4.2 EVM Compatibility
Developers used to Ethereum can deploy Solidity smart contracts directly to NHBChain without modification using standard tools (Hardhat, Foundry). The chain sponsors contract execution, maintaining the zero-fee user experience.

---

## 5. Ecosystem Primitives & Functionality

### 5.1 The Swap Ledger (On-Chain Offramps)
Users can swap NHB for external assets (like USDT or fiat) natively.
*   **Development:** Use `nhb_getSwapQuote` to fetch a TWAP-secured exchange rate, `nhb_checkSwapAllowance` for permissions, and sign a native `TxTypeSwapBurn` or `TxTypeSwapMint` to execute the exchange.
*   **Security:** Swaps are protected by Time-Weighted Average Price (TWAP) guards to strictly prevent arbitrage and flash-crash exploits.

### 5.2 P2P Escrow Engine
A natively integrated escrow protocol protects peer-to-peer trades.
*   **Development:** Utilize `nhb_escrowCreate`, `nhb_escrowFund`, and `nhb_escrowRelease`.
*   **Milestones:** Escrows can be milestone-based (`TxTypeEscrowMilestoneCreate`), releasing funds only when verifiable deliverables are met.
*   **Dispute Resolution:** Built-in mediation logic handles contested transactions securely.

### 5.3 Staking and Lending
*   **Staking:** Users bond `ZNHB` to validators via `TxTypeStakeDelegate`. Frontend clients use `nhb_getBalance` (reading `unbondingCompletesAt` and `pendingStakingRewards`) to visualize yield intuitively.
*   **Lending:** A functional treasury system allows utilizing ZNHB as collateral to borrow NHB working capital via explicit `lending_borrowNHB` logic.

---

## 6. Network Security & Fund Protection

NHBChain ensures institutional-grade security mechanisms to protect user liquidity and network consensus:

*   **Validator Slashing:** Validators who behave maliciously (double-signing) or fall inactive (missing heartbeats) are actively slashed.
*   **Compliance Bridge (Sanctions & KYB):** The RPC gateway inherently connects to a `Sanctions Checker` API. Transactions attempting to interact with OFAC-sanctioned addresses are rejected at the protocol layer.
*   **Rate Limits:** Read-heavy queries and mempool injections are throttled via IP and Identity-based CIDR limits.
*   **Zero-Knowledge Environment Flags:** The network refuses to boot if the production environment variable (`NHB_ENV`) is missing, explicitly preventing accidental dev-mode exposures on mainnet architectures.

---

## 7. Governance Structure

NHBChain operates as a decentralized, community-driven network managed through on-chain proposals. This ensures parameters can evolve dynamically without hard forks.

*   **Proposal Creation:** Any user holding sufficient ZNHB can submit a proposal via `TxTypeGovernancePropose`.
*   **Voting Mechanism:** `TxTypeGovernanceVote` allows ZNHB stakers to vote `Yes`, `No`, or `Abstain`. Voting power is weighted linearly by staked `ZNHB`.
*   **Execution:** Passed proposals are executed natively by the chain (`TxTypeGovernanceExecute`).
*   **Configurable Parameters:** Governance directly controls the maximum transactions per block (`MaxTxs`), Swap TWAP limits, unbonding durations, and lending breaker thresholds. 
*   **Frontend Integration:** The portal uses `nhb_governanceProposal` and `nhb_governanceList` to render the democratic dashboard for community voting.

---

## 8. Conclusion & Marketing Narrative

**For Investors & Stakeholders:** NHBChain is not just another token speculatively trading in a vacuum. It is a highly optimized, compliance-ready financial engine capable of replacing Visa/Mastercard interchange fees for merchants, instantly settling cross-border remittances, and generating organic yield via real-world economic activity.

**For Developers:** Build your storefronts, POS terminals, and mobile wallets utilizing email-based identity, free transactions, and our bulletproof escrow APIs without dragging your users through the complexities of blockchain UX.
