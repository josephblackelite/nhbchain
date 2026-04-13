# Codex Audit Report

Date: 2026-04-11
Repo: nhbchain
Scope: transfer engine, POS and atomic payments, swaps, escrow, governance, POTSO rewards, live-upgrade risk, and financial-layer readiness.

## Executive Summary

Current verdict: the codebase has several well-designed financial building blocks, but the live transfer/payment path is not yet safe enough to position as a hardened production financial settlement layer without coordinated fixes.

The strongest parts today are:
- escrow, which has a real state machine, frozen dispute policy, quorum signatures, and idempotent transitions
- POS authorization/capture/void lifecycle, which does atomic balance updates with rollback logic
- swap risk controls, which include signer checks, stale-proof checks, deviation limits, partner quotas, and nonce persistence
- governance policy admission, allow-lists, quorum/threshold settings, and timelock flow
- POTSO treasury/ledger model, which is deterministic and auditable in principle

The weakest parts today are:
- the signed transfer domain for POS-tagged transfers
- NHB transfer accounting and hidden economic behavior
- RPC input parsing correctness
- protocol/documentation mismatches in governance and payments
- operational hardening around secrets, startup requirements, and incomplete swap abort handling

If this repository is already online with live nodes, then yes: some of the fixes are hard-fork territory. The main hard-fork items are:
- fixing `IntentRef` so it is actually covered by the signed transaction hash
- correcting NHB gas accounting
- changing or removing the hidden 1.5% routing tax

Other fixes are not hard forks if implemented carefully:
- panic-proofing malformed address handling
- correcting RPC hex parsing
- startup and secret-management hardening

Heartbeat encoding sits in the middle: it can be made backward-compatible with dual decoding, but if changed one-sided on a live heterogeneous network it can still create block acceptance splits.

## High Issues Already Confirmed

1. `IntentRef` is not covered by the V3 signed transaction hash.
Reference: `core/types/transaction.go:121-173`, especially `IntentExpiry` at line 136 and omission of `IntentRef`.
Impact:
- POS-tagged transfers do not cryptographically bind the merchant payment intent reference that downstream systems rely on.
- A relay or middleware can swap which intent gets consumed without invalidating the sender signature.
- This breaks the documented claim that the on-chain envelope fields are signed together with the transaction body.
Live-network classification:
- hard fork / coordinated protocol upgrade if fixed at consensus level for live nodes

2. Malformed `To` or `Paymaster` lengths can panic hashing and submission.
Reference: `core/types/transaction.go:139-142`, `core/types/transaction.go:169-173`, `rpc/http.go:2651`.
Impact:
- request-triggered denial of service
- malformed 21-byte values can panic before input validation completes
Live-network classification:
- not a hard fork if fixed by safe validation and panic-proof hashing for invalid inputs only

3. RPC hex parsing misreads many `0x`-prefixed values.
Reference: `rpc/http.go:2516-2558`, especially `strings.ContainsAny(strings.ToLower(v), "abcdef")` at lines 2524 and 2547.
Impact:
- standard JSON-RPC hex values such as `0x10` and `0x5208` are parsed as decimal unless they contain `a-f`
- gas, amount, nonce, and signature-domain inputs can be interpreted incorrectly
- transfer failures are expected from normal client payloads
Live-network classification:
- not a hard fork
- API compatibility and correctness fix

4. NHB transfer gas accounting is internally inconsistent and already fails tests.
Reference: `core/state_transition.go:1635-1649`, `core/transfer_znhb_test.go:522-567`.
Impact:
- sender balance is checked against `value + gas`, but only `value` is debited in the transfer branch
- comments claim `applyTransactionFee` will handle gas, but that function is not the generic NHB gas debit path promised here
- this is a likely direct cause of transfer failures or inconsistent balance outcomes
Live-network classification:
- hard fork / coordinated protocol upgrade because it changes valid state transitions and balances

## Additional Material Findings

5. A hidden 1.5% routing tax is hardcoded into transfers after block height `> 1000`.
Reference: `core/state_transition.go:1602-1624`, `core/state_transition.go:2041-2055`, `core/state_transition.go:5232-5235`.
Impact:
- NHB and ZNHB transfers silently receive `value - tax`
- the fee rate is currently a stub returning `150` basis points rather than a real governed lookup
- this is an undisclosed economic rule unless clearly documented to all users
Live-network classification:
- hard fork if changed or removed on a live network

6. Transfer events report the gross amount even when recipients receive the net amount after tax.
Reference: `core/state_transition.go:1698-1708`, `core/state_transition.go:2088-2096`.
Impact:
- explorers, merchant reconciliation, accounting, and tax reporting can all drift from actual balances
- this is unacceptable for a financial ledger if event consumers are treated as settlement truth
Live-network classification:
- event-only correction is likely not a hard fork if events are not part of consensus validity

7. Heartbeat encoding is inconsistent across producer and consumer paths.
Reference: `tests/system/quota_test.go:54-55`, `core/node.go:4105-4106`, `core/state_transition.go:3330-3332`.
Impact:
- producers marshal `HeartbeatPayload` as JSON
- execution decodes with RLP
- system tests already fail, so the POTSO/engagement path is not protocol-stable
Live-network classification:
- compatibility-sensitive
- safest path is dual decode plus a canonical future encoding

8. Governance deposit behavior does not match governance documentation.
Reference:
- docs promise return/slash semantics: `docs/governance/overview.md:34-39`
- code only unlocks deposits when proposals pass: `native/governance/engine.go:1603-1625`
- only observed unlock call: `native/governance/engine.go:1622`
Impact:
- rejected or failed-quorum proposal deposits appear to remain locked with no implemented return/slash path in the reviewed code
- this can trap proposer capital and undermine governance usability and fairness
Live-network classification:
- not necessarily a hard fork if fixed as module bookkeeping, but it is a serious financial/governance anomaly

9. Swap cash-out abort is documented but not implemented.
Reference:
- docs: `docs/swap/stable-ledger.md:42-43`
- create/settle paths exist: `native/swap/stable_store.go:177-206`, `native/swap/stable_store.go:302-415`
- no abort implementation was found in `native/swap/stable_store.go`
Impact:
- a cash-out that needs to be cancelled or failed cleanly lacks a reviewed on-chain unwind path
- this creates operational and customer-service risk for a redemption rail
Live-network classification:
- not a hard fork if added as a new safe state transition

10. Sensitive local artifacts remain exposed in the workspace and some are not ignored.
Observed files include `.env`, `wallet.key`, `nhbchain.pem`, `token.txt`, and `KEYS & ENODE - SUNDAY 14-3.txt`.
Current `.gitignore` covers `.env`, `wallet.key`, and `nhbchain.pem`, but not all credential-like artifacts.
Impact:
- accidental credential disclosure
- validator or operator compromise risk
Live-network classification:
- not a hard fork

11. Node startup hard-fails unless `NHB_ENV` is set.
Reference: `core/node.go:439`.
Impact:
- brittle ops and test behavior
- makes recovery and portability worse
- already breaks package tests in this repo
Live-network classification:
- not a hard fork

## Hard-Fork Classification

These changes require coordinated protocol activation if the network is already live:
- `IntentRef` hash-domain repair
- NHB gas-accounting correction
- changing or removing the current routing-tax behavior
- any change that alters whether currently valid transfer or heartbeat transactions are accepted during block execution

These changes do not require a hard fork if implemented carefully:
- reject malformed `To` and `Paymaster` lengths before any panic-prone hashing path
- correct JSON-RPC hex parsing
- fix startup/env defaults and secret hygiene
- add swap abort handling
- align event payloads with actual net settlement amounts if events are not part of consensus validity

Compatibility-sensitive but possibly upgrade-safe if done with backward compatibility:
- heartbeat JSON/RLP mismatch: accept both temporarily, standardize one canonical encoding later
- transaction-hash migration: only safe if introduced through a new tx version or explicit activation height, not by silently redefining the existing hash

## Financial-Layer Readiness Assessment

### Transfers

Current status: not ready to be treated as the primary safe settlement rail until the high issues are fixed.

Why:
- the NHB transfer path has inconsistent gas accounting
- there is a hidden tax branch after block height `> 1000`
- gross-vs-net event reporting is inconsistent
- standard RPC clients can submit numerically misparsed payloads
- POS-tagged transfer signatures do not fully bind the merchant intent data they are supposed to protect

For a financial network, transfers must provide:
- exact debit-credit determinism
- exact event/receipt/accounting consistency
- stable signature domains
- explicit and governed fee behavior
- resilient invalid-input handling

At the moment the transfer engine does not fully meet that bar.

### POS And Atomic Payments

There are two distinct payment surfaces here, and they should not be treated as equally mature.

1. POS authorization lifecycle (`native/pos/auth.go`)
- This is one of the healthier modules.
- It locks ZNHB, supports capture/void/expiry, emits lifecycle events, and includes rollback logic around persistence failures.
- References: `native/pos/auth.go:145-217`, `native/pos/auth.go:225-319`, `native/pos/auth.go:321-390`, `native/pos/auth.go:491-549`

2. Direct POS-tagged transfers using `intent_ref`
- This is not safe enough yet because `IntentRef` is not signed inside the V3 hash.
- The intent registry exists and is consumed after execution (`core/state_transition.go:1484-1490`), which is good, but the sender signature still does not bind the exact intent reference used for settlement.

Conclusion:
- the pre-authorize/capture/void path is much closer to financial-grade behavior than the transfer-based POS path
- if you want safe retail payment flows, lean on the authorization lifecycle model rather than relying on plain transfer plus `intent_ref` until the hash-domain issue is versioned and fixed

### Atomic Payments Per Second And Throughput

Documented performance targets:
- block time target: 2.5s +/- 0.5s
- sustained throughput target: 300 TPS
- finality lag target: under 5s
Reference: `docs/perf/baselines.md:1-11`

POS QoS targets:
- priority lane reserved at 15% of block capacity by default
- p95 POS finality target <= 5,000 ms
- readiness harness drives a configurable `--rate 600` load to test the lane
Reference: `docs/specs/pos-qos.md:1-27`, `docs/specs/pos-qos.md:44-64`, `docs/slas/finality-and-qos.md`, `tests/posreadiness/qos/qos_test.go:126-145`

Inference:
- at the documented 300 TPS chain target and 15% reservation, the guaranteed POS slice is roughly 45 POS tx/s before spillover from unused normal-lane capacity
- that is acceptable for moderate-volume merchant networks, kiosk lanes, and controlled payment corridors
- it is not remotely at Visa-scale or global open retail scale

Important limitation:
- what exists in the repo today is a target and a readiness harness, not a published production benchmark pack proving sustained real-world throughput under adversarial load, failover, and mixed workloads

Operationally, that means:
- this can function as a moderate-throughput financial transaction layer if the transfer-consensus issues are fixed
- it should not yet be marketed as a battle-proven high-scale payment network without published soak and recovery data

### Escrow

Current status: structurally strong and one of the better candidates for financial use once the base transfer layer is corrected.

Strengths:
- explicit create/fund/release/refund/expire/dispute/resolve state machine
- idempotent transitions
- deterministic escrow identifiers
- separate vault address per token
- arbitrator-threshold and realm-bound checks
- frozen dispute policy with signature quorum validation
- clear fee-routing logic

References:
- lifecycle entry points: `native/escrow/engine.go:594-847`
- quorum verification: `native/escrow/engine.go:953-998`
- signed dispute resolution: `native/escrow/engine.go:1180-1227`

Financial suitability:
- good for controlled bilateral settlement, dispute handling, and marketplace workflows
- still depends on correct underlying token transfer accounting and vault solvency
- should be paired with independent vault reconciliation and role audits

### Swaps

Current status: reasonably safe as a controlled, operator-backed mint/redemption rail, but not a trustless decentralized swap engine.

What is implemented well:
- price proofs can enforce domain, pair, provider, signer, freshness, and deviation checks
- stable ledger tracks deposit vouchers, cash-out intents, payout receipts, and treasury soft inventory
- partner quota and API nonce persistence exist in service storage tests

References:
- price-proof checks: `native/swap/engine.go:15-32`, `native/swap/engine.go:81-171`
- risk invariants: `tests/swap/risk_invariants_test.go:23-84`
- stable ledger design: `docs/swap/stable-ledger.md:1-55`
- quota and nonce persistence: `services/swapd/storage/storage_test.go:167-265`

What this means financially:
- this is closer to a supervised treasury/redemption system than an AMM
- its safety comes from reconciliation, signer hygiene, inventory controls, and fiat operations, not from trustless market-making
- that is acceptable if positioned honestly as a regulated or semi-custodial settlement bridge

Main open issue:
- abort/cancel flow is still missing from the reviewed store implementation, which is a real operational gap for failed or reversed redemptions

### Governance

Current status: conceptually solid, but not yet clean enough to be the sole control plane for a live financial network without implementation cleanup.

Strengths:
- proposal kind validation and parameter allow-lists
- quorum and pass-threshold controls
- timelock queue and execution flow
- treasury allow-list concept
- authenticated governance service with persisted nonce handling

References:
- proposal submission and deposit lock: `native/governance/engine.go:1299-1389`
- voting from prior POTSO snapshot: `native/governance/engine.go:1406-1462`
- tally/finalize: `native/governance/engine.go:1549-1645`
- queue/execute: `native/governance/engine.go:1653-1761`
- authenticated service and nonce persistence: `services/governd/server/server.go:64-68`, `services/governd/server/server.go:287-339`

Financial/governance concerns:
- voting power is tied to prior POTSO weight, which blends stake and engagement; this is a policy choice, not necessarily a bug, but it creates a power structure that should be disclosed clearly
- deposit lifecycle implementation does not yet match the documented return/slash model
- because governance is expected to control fees and policy, documentation-to-code drift here is a governance risk, not just a docs issue

### POTSO Rewards / POTSO Engine

Current status: promising as a treasury-funded loyalty/reward engine, but not yet stable enough operationally because the heartbeat path is inconsistent.

Strengths:
- deterministic weighting with documented formula and tie-breakers
- treasury-backed rewards rather than pooled-user-fund mechanics
- claim/history/export model is audit-friendly
- heartbeat rate limiting and abuse telemetry exist

References:
- heartbeat engine and rate limiting: `native/potso/engine.go:12-130`
- weighting math: `native/potso/metrics.go:13-332`
- treasury and controls: `docs/potso/treasury-and-controls.md`
- compliance framing: `docs/potso/compliance-overview.md`

Weakness:
- the heartbeat encoding mismatch means an important engagement input is not protocol-stable today

Financial suitability:
- suitable as a controlled loyalty/rebate mechanism if treasury funding, claim controls, and heartbeat consistency are fixed
- not a direct settlement risk like transfers, but still material because governance voting power and ecosystem incentives depend on it

## Can This Function As A Safe Financial Network?

Not yet as-is.

It can become a credible controlled financial network if the following conditions are met:
- transfer semantics are corrected and made explicit
- payment intent binding is fixed through a coordinated versioned upgrade
- hidden fees are either formalized through governance and disclosed everywhere or removed
- RPC input handling is corrected so standard clients behave deterministically
- event outputs are made reconcilable with actual balances
- swap abort and operational rollback paths are completed
- governance deposit economics are implemented exactly as documented
- the heartbeat/rewards path is made protocol-stable
- treasury, vault, and secret-management controls are tightened
- sustained throughput and failure-recovery benchmarks are published

If those are done, the architecture can support:
- merchant POS authorization and capture
- bilateral escrow settlement
- supervised fiat/stable redemption flows
- treasury-funded rewards
- on-chain governance for bounded policy changes

Without those fixes, it is still too easy for payment reconciliation, balances, or operator assumptions to diverge from chain reality.

## Recommended Fix Program

### Immediate No-Fork / Rolling Fixes

1. Reject malformed `To` and `Paymaster` values before any hash or sender recovery path can panic.
2. Correct JSON-RPC numeric parsing so all `0x`-prefixed values are interpreted as hex consistently.
3. Make heartbeat decoding backward-compatible, then publish one canonical encoding.
4. Implement swap cash-out abort and escrow release path for failed redemptions.
5. Remove or ignore all credential-like local artifacts and tighten operator secret hygiene.
6. Replace the mandatory `NHB_ENV` hard fail with a safer default or clearer bootstrap profile.
7. Align transfer events and merchant-facing receipts with net settlement amounts.

### Coordinated Upgrade / Hard-Fork Items

1. Introduce a versioned transaction hash fix so `IntentRef` is signed for POS-tagged transfers.
2. Correct NHB gas accounting with an activation height and replay-tested migration plan.
3. Replace the hardcoded routing-tax stub with a real governed parameter, or remove it through an explicit upgrade.
4. Re-test all transfer, POS, explorer, and accounting paths against mixed old/new-node scenarios before rollout.

### Financial Controls And Operational Adjustments

1. Publish a formal fee policy document covering every transfer path and whether gross or net values appear in receipts and events.
2. Add daily treasury/vault reconciliations for escrow, swap, and POTSO addresses.
3. Finish governance deposit return/slash semantics to match docs and regulator-facing disclosures.
4. Publish benchmark evidence for sustained throughput, POS latency, recovery time, and node failover.
5. Audit signer key custody for swap price proofs, treasury automation, and governance service keys.

## Bottom Line

This is not a hopeless codebase. In fact, several modules show clear thought and good financial-system instincts. But the transfer/payment core still has protocol-level issues serious enough that I would not describe the current live system as fully safe for production financial settlement until they are corrected.

If the network is already online, treat the `IntentRef` fix, NHB gas fix, and routing-tax fix as protocol-upgrade work, not ordinary bugfix work.

The repo is closest to being viable as:
- a moderate-throughput, controlled financial rail
- with strong escrow and POS authorization features
- supervised redemption/swap flows
- treasury-funded rewards
- governance-driven parameter control

It is not yet ready to be treated as a fully hardened general-purpose payment network without a coordinated cleanup of the issues above.
