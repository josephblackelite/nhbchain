# Creator Module Security Notes

The creator economy introduces new flows for capital and engagement. Operators should harden deployments around the following safeguards.

## Smart Account Hygiene

* **Authentication** – All RPC endpoints require the existing bearer token. Rotate credentials regularly and restrict access to trusted apps (e.g. the devnet studio).
* **Input validation** – The engine enforces non-empty content IDs and positive amounts. Clients should still validate locally to catch mistakes before signatures are gathered.
* **Idempotency** – Content IDs are unique. Publishing with an existing ID will fail, preventing accidental overwrites.

## Treasury & Balance Protection

* **Sufficient funds** – Tipping and staking debit the caller immediately. Failed balance checks surface as validation errors; clients should present clear messaging to avoid repeated retries.
* **Rate limits** – The RPC server already enforces a sliding window (5 tx/min). Consider lowering this in public devnets to reduce griefing and wash trading.
* **Reward configuration** – Staking yield is controlled in-code (2.5% BPS in this build). Networks can fork to adjust or gate staking entirely if needed.

## Anti-Abuse Guidance

* **Content moderation** – The chain stores only references. Operators should run off-chain scanners to flag abusive URIs or metadata and surface trust scores in front-ends.
* **Sybil detection** – Pair creator staking with existing engagement metrics and sanctions lists. High-velocity staking from new accounts should trigger monitoring before payouts are claimed.
* **Withdrawal monitoring** – The `creator_payouts` claim path emits `creator.payout.accrued`. Hook this into fraud analytics so suspicious drains are quarantined quickly.

## Devnet Playbook

* Spin up the JSON-RPC server with auth enabled.
* Deploy the `/examples/creator-studio` workspace and follow the “publish → tip → stake → payout” walkthrough.
* Use the emitted events (see [`docs/creator/overview.md`](./overview.md)) to verify indexing coverage and ensure dashboards capture every lifecycle edge.

By following these practices the creator module can be rolled out on devnet with predictable behaviour and clear recovery paths for edge cases.
