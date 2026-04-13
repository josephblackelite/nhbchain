# POTSO Stakeholder Briefing

The POTSO telemetry meters provide transparent evidence of validator and participant activity without issuing rewards. This briefing explains the controls and assurances relevant to auditors, regulators, investors, and end users.

## Auditors

- **Deterministic records** – All meters are stored in the canonical state trie alongside account data. Every heartbeat updates the trie deterministically and emits the `potso.heartbeat` event, allowing auditors to cross-check node logs against state roots.
- **Replay and spam protection** – Heartbeats must reference a known block hash, include a timestamp within ±120 seconds, and respect the 60-second minimum interval. Replays or forged payloads are rejected before touching state.
- **Signature verification** – Payloads are signed with the participant's NHB key. The RPC handler verifies the signature before forwarding the request to the node, ensuring the meters reflect only self-attested activity.

## Regulators

- **No rewards** – The module records uptime, transaction counts, and escrow touches, but it does not distribute tokens or modify financial positions. It acts as a telemetry layer for future compliance or incentive programs.
- **Traceability** – Each heartbeat is linked to a specific block height and stored timestamp, enabling precise reconstruction of participant availability windows. Combined with transaction counts, regulators can assess service continuity.
- **Data minimisation** – Only aggregate counters are stored. No personal data beyond the pseudonymous NHB address is recorded.

## Investors

- **Operational insight** – Leaderboards derived from `potso_top` highlight the most engaged validators, helping investors evaluate operator reliability without relying on self-reported metrics.
- **Risk monitoring** – Sudden drops in uptime or escrow engagement serve as early warning signals for validator issues. Investors can subscribe to heartbeat events to trigger alerts.
- **Future extensibility** – The scoring function isolates raw counters (`rawScore`) from the published score (`score`), allowing future weight adjustments or caps without rewriting history.

## Consumers

- **Transparency** – Wallets and explorers can display daily uptime and engagement scores, giving users confidence that validators remain online and active.
- **Fairness** – Rate limits and hash checks prevent malicious actors from inflating their uptime or claiming another participant's activity.
- **Ease of verification** – The public RPC endpoints `potso_userMeters` and `potso_top` allow anyone to inspect raw counters without special permissions.

## Key Takeaways

- POTSO meters are immutable, verifiable, and independent of reward distribution.
- Security controls (signatures, block hash verification, timestamp tolerances) keep the data set trustworthy and resistant to spam.
- Multiple audiences can rely on the same telemetry for compliance, investment decisions, and user-facing transparency.
