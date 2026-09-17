# Engagement Heartbeat and Scoring Program

## Overview
The engagement program introduced in this branch wires validator heartbeats into the ledger, tracks daily participation across transaction categories, and rolls those observations into an exponentially weighted score that caps daily credit.【F:core/state_transition.go†L642-L712】【F:core/state_transition.go†L886-L985】 Two authenticated RPC endpoints allow validators to register devices and enqueue heartbeats, while on-chain events expose both the raw heartbeats and the derived score updates for downstream consumers.【F:rpc/http.go†L248-L259】【F:rpc/engagement_handlers.go†L28-L75】【F:core/events/engagement.go†L10-L63】

## System Components

### Configurable scoring parameters
The scoring engine is driven by an `engagement.Config` structure that tunes category weights, the daily cap, EMA decay, and anti-spam thresholds. A safe default exists for testing, and defensive validation prevents nonsensical or unsafe inputs.【F:core/engagement/config.go†L8-L52】 The configuration is cached inside the state processor and can be swapped atomically once network governance agrees on new parameters.【F:core/state_transition.go†L60-L101】

### Device registration and heartbeat gating
Validators enroll heartbeat devices through the engagement manager, which issues cryptographically strong bearer tokens, stores the association, and enforces monotonically increasing timestamps alongside minimum spacing requirements to defeat replay and spam before traffic hits consensus.【F:core/engagement/manager.go†L13-L123】 These semantics are regression tested for both rate limiting and replay protection.【F:core/engagement/manager_test.go†L8-L46】 The node instantiates the manager with the active configuration so runtime changes take effect immediately.【F:core/node.go†L31-L78】

### On-chain accumulation of daily metrics
When a heartbeat transaction is applied, the state processor verifies on-chain rate limits, clamps the credited minutes, advances the sender nonce, and records the latest timestamp. It then emits a heartbeat event for observability.【F:core/state_transition.go†L642-L712】 Every transaction pathway that implies validator participation—EVM calls, identity, escrow, and governance operations—funnels into `recordEngagementActivity`, incrementing per-day counters for minutes, transaction count, escrow touchpoints, and governance events.【F:core/state_transition.go†L280-L349】【F:core/state_transition.go†L1008-L1030】 The account metadata schema persists these counters alongside the current day identifier and prior heartbeat timestamp so state reads reflect pending accruals.【F:core/state/accounts.go†L24-L173】【F:core/types/account.go†L6-L17】

### Daily rollovers, caps, and EMA scoring
Whenever a heartbeat or activity record crosses a day boundary, the processor rolls accumulated buckets forward. Raw scores are computed as a weighted sum of category counts, capped at the configured daily maximum, and blended with the prior score via an exponential moving average to smooth volatility.【F:core/state_transition.go†L886-L985】 Finished updates trigger `engagement.score_updated` events and reset intra-day counters, ensuring daily isolation.【F:core/state_transition.go†L986-L1005】 Comprehensive state tests cover the EMA math, per-day resets, and cap enforcement across multiple simulated days.【F:core/engagement_state_test.go†L15-L203】

### RPC workflow and authentication
Two RPC methods—`engagement_register_device` and `engagement_submit_heartbeat`—require the global RPC token, deserialize validated payloads, and delegate to the node. Registration binds a device identifier to the validator’s bech32 address and returns the manager-issued token, while heartbeat submission checks the credential pair, constructs a signed heartbeat transaction, and places it in the mempool for consensus.【F:rpc/http.go†L248-L259】【F:rpc/engagement_handlers.go†L28-L75】【F:core/node.go†L540-L587】 The RPC server enforces a five-transaction-per-minute quota per client and rejects new submissions once `[mempool] MaxTransactions` is reached, so operators should size their mempool and proxy allow-lists accordingly when onboarding large validator fleets.【F:rpc/http.go†L32-L38】【F:config/config.go†L128-L132】 Heartbeat payloads include the device ID and optional timestamp override, defaulting to the manager-approved value to keep replay guards consistent.【F:core/types/heartbeat.go†L3-L6】【F:core/engagement/manager.go†L81-L123】

### Event model and observability
Two dedicated events surface engagement data: `engagement.heartbeat` captures the address, device identifier, minutes credited, and timestamp for every processed heartbeat, while `engagement.score_updated` discloses the raw contribution, previous score, and new EMA per day. Both events normalize addresses to bech32 strings for API consumers.【F:core/events/engagement.go†L10-L63】 The state processor emits heartbeats immediately after state mutation and releases score updates as soon as day rollovers complete, guaranteeing chronological integrity.【F:core/state_transition.go†L695-L705】【F:core/state_transition.go†L986-L1005】

## Stakeholder-Focused Analysis

### Auditors
* **Deterministic scoring:** The weighted sum, cap, and EMA formulation is explicitly parameterized and committed in state, enabling auditors to recompute scores offline with the same logic.【F:core/state_transition.go†L886-L985】
* **Traceable state:** Per-day counters, last heartbeat timestamps, and scores are persisted in account metadata, allowing full provenance of each day’s carry-over and simplifying historical reconstruction.【F:core/state/accounts.go†L24-L173】
* **Replay/rate-limit controls:** Off-chain gating plus on-chain verification ensure only authorized, monotonically increasing heartbeats are accepted, mitigating manipulation risk before and after consensus.【F:core/engagement/manager.go†L81-L123】【F:core/state_transition.go†L663-L712】
* **Testing evidence:** Unit tests demonstrate rejection of rapid or duplicate heartbeats and verify EMA cap behavior, providing assurance that controls operate as intended.【F:core/engagement/manager_test.go†L8-L46】【F:core/engagement_state_test.go†L15-L203】

### Regulators
* **Fair participation incentives:** Configurable weights let governance calibrate emphasis on uptime, transaction facilitation, escrow mediation, and governance activity to align with regulatory expectations for validator responsibilities.【F:core/engagement/config.go†L8-L35】【F:core/state_transition.go†L280-L349】
* **Daily caps and smoothing:** Enforcing a daily score ceiling and EMA smoothing limits sudden swings that could incentivize risky behavior, supporting stable reward structures.【F:core/engagement/config.go†L8-L35】【F:core/state_transition.go†L886-L937】
* **Access controls:** RPC endpoints require bearer authentication and validator address checks, preventing unauthorized device enrollment or heartbeat submission—critical for maintaining reliable operational metrics.【F:rpc/http.go†L248-L259】【F:core/node.go†L540-L587】

### Customers and Ecosystem Partners
* **Operational transparency:** Engagement events provide real-time insight into validator uptime and activity, which downstream dashboards can leverage for SLA monitoring or partner reporting.【F:core/events/engagement.go†L10-L63】
* **Consistent metrics:** The account schema exposes current-day counters and historical scores through existing account queries, enabling customer support to answer validator performance questions without bespoke tooling.【F:core/state/accounts.go†L86-L123】
* **Graceful day transitions:** Automatic rollover logic resets daily counters at UTC midnight based on timestamps, ensuring customer reports align with calendar days without manual intervention.【F:core/state_transition.go†L939-L985】

### Developers
* **Extensible configuration:** Developers can adjust weights, caps, or intervals via `SetEngagementConfig` and rely on validation to catch invalid parameterizations during rollout testing.【F:core/state_transition.go†L60-L101】【F:core/engagement/config.go†L37-L52】
* **Integration hooks:** The engagement manager exposes deterministic APIs for registration and heartbeat submission; developers can mock the `now` function for deterministic tests, as shown in the suite.【F:core/engagement/manager.go†L42-L123】【F:core/engagement/manager_test.go†L8-L46】
* **State access patterns:** Account metadata includes all engagement fields, so RPC or CLI tooling can surface real-time counters without touching raw trie nodes.【F:core/state/accounts.go†L24-L173】
* **Event-driven workflows:** By subscribing to the new event types, services can trigger notifications, adjust reputation scores, or feed analytics pipelines as soon as heartbeats and score updates are emitted.【F:core/events/engagement.go†L10-L63】【F:core/state_transition.go†L695-L1005】

### Peer Validators and Node Operators
* **Device lifecycle:** Validators must register each heartbeat device against their own address; the node enforces this by comparing the RPC-supplied address with the validator’s consensus key to prevent misbinding.【F:core/node.go†L540-L548】
* **Submission guarantees:** Successful heartbeat submissions return the timestamp queued on-chain, aligning device clocks with the authoritative schedule and clarifying when the next heartbeat may be sent.【F:core/node.go†L551-L587】【F:core/engagement/manager.go†L81-L123】
* **Activity credit:** Beyond heartbeats, validators accrue engagement from executing transactions, managing escrow, and participating in governance, rewarding holistic contributions to network health.【F:core/state_transition.go†L280-L349】

### Investors and Analysts
* **Quantifiable engagement:** The EMA score provides a smoothed indicator of validator reliability and ecosystem participation, suitable for dashboards that correlate engagement with staking outcomes or token distribution policies.【F:core/state_transition.go†L886-L937】
* **Risk mitigation:** Caps and anti-replay controls reduce the chance of inflated metrics, preserving the credibility of engagement-linked incentives that investors may rely on for evaluating validator cohorts.【F:core/engagement/config.go†L8-L35】【F:core/engagement/manager.go†L81-L123】
* **Event visibility:** Public events expose heartbeat cadence and score trajectories, enabling analytics firms to track validator performance trends without privileged access.【F:core/events/engagement.go†L10-L63】

## Operational Considerations
* **Timekeeping:** Heartbeat minutes are derived from timestamp deltas and clamped to the configured maximum, so operators should maintain accurate device clocks to avoid under-crediting.【F:core/state_transition.go†L675-L688】
* **Config governance:** Because configuration changes affect scoring across the network, updates should be coordinated and deployed simultaneously to all nodes using the provided setter to ensure consensus on reward calculations.【F:core/state_transition.go†L60-L101】
* **Telemetry:** Emitted events can be indexed to monitor for missed heartbeats or abrupt score drops, supporting proactive incident response and compliance reporting.【F:core/state_transition.go†L695-L1005】
* **Testing regimen:** Existing unit tests cover rate limits, replay detection, EMA correctness, and daily caps. Teams should extend these scenarios when adjusting parameters or integrating additional engagement signals.【F:core/engagement/manager_test.go†L8-L46】【F:core/engagement_state_test.go†L15-L203】

## Conclusion
This engagement infrastructure combines authenticated device heartbeats, multi-category activity tracking, and transparent eventing to deliver a defensible measure of validator participation. The design balances operational practicality with auditability, providing clear integration points for tooling while supporting governance control over scoring economics.【F:core/engagement/config.go†L8-L52】【F:core/state_transition.go†L642-L1030】【F:core/events/engagement.go†L10-L63】
