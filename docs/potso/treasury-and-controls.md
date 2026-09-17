# Treasury Operations & Controls

The POTSO reward pipeline moves ZapNHB from the configured treasury account to winning addresses. Settlement mode controls *when*
this movement happens, but finance teams retain responsibility for cash management and controls.

## Key Roles

| Role | Responsibilities |
|------|------------------|
| Treasury Operations | Fund the `potso.rewards.TreasuryAddress`, monitor balances, and approve claim batches. |
| Compliance / Risk | Review claim ledgers, flag anomalous payouts, and maintain access policies for RPC credentials. |
| Engineering | Maintain node infrastructure, configure payout mode, monitor RPC/webhook health, and ship CLI/bot tooling. |

## Balance Management

* **Working capital:** keep at least one epoch of headroom in auto mode, and enough to satisfy the largest expected claim batch in
  claim mode. `potso_export_epoch` totals help size replenishments.
* **Top-ups:** treasury funding is a standard ZapNHB transfer into the configured address. No on-chain configuration change is
  required.
* **Shortfalls:**
  * Auto mode aborts epoch processing with `potso.ErrInsufficientTreasury`.
  * Claim mode rejects individual claims with `INSUFFICIENT_TREASURY`; the ledger entry remains pending.
  * In both cases: fund the treasury and retry (epoch processing resumes automatically on the next block; claims can be re-run via
    CLI or automation).

## Ledger & Audit Trail

* **Claim ledger:** `PotsoRewardsGetClaim(epoch, addr)` exposes `{amount, claimed, claimedAt, mode}`. The ledger is written at
  epoch close (for both modes) and updated when settlement happens.
* **History ledger:** `PotsoRewardsHistory(addr)` returns a chronological log of paid entries including the mode. Use it for user
  statements, reconciliations, and regulator reports.
* **Exports:** `potso_export_epoch` produces a CSV suitable for archival storage and cross-checking bank statements.
* **Events:** subscribe to `potso.reward.ready` (claim mode) and `potso.reward.paid` to build downstream audit logs and alerting.

## Controls Checklist

1. **Access separation:** store `NHB_RPC_TOKEN` in a secrets manager. Only treasury automation and approved operators should be
   able to call `potso_reward_claim`.
2. **Dual approval (optional):** in claim mode, route ready notifications through internal workflow tools (e.g., Jira, GRC
   systems) before running the claim bot.
3. **Reconciliation cadence:**
   * Daily: export the most recent epoch and reconcile against treasury debits.
   * Monthly: roll up `PotsoRewardsHistory` per address to satisfy loyalty/compliance reporting.
4. **Incident response:** if a claim was processed in error, reverse it using standard ZapNHB transfer tooling and mark the ledger
   entry in the downstream accounting system. On-chain the record remains immutable but subsequent exports show the corrective
   transfer.
5. **Key rotation:** rotate the signing keys used by automation that submits claims at least quarterly. Update the CLI/automation
   configuration accordingly.

Following these controls keeps the POTSO reward program aligned with finance policies while delivering predictable settlement
experience to participants.
