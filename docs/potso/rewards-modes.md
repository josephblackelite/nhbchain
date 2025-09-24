# POTSO Reward Payout Modes

The POTSO module now supports two settlement strategies for distributing ZapNHB rewards. Both modes use the same emission
math and winner selection logic. The only difference is *how* the computed balances are transferred from the treasury to
participants.

## Mode Comparison

| Mode  | Treasury Movement | Winner Experience | Operational Impact |
|-------|-------------------|-------------------|--------------------|
| `auto`  | Treasury debited and winners credited automatically during epoch close. | Funds arrive without user action. | Requires the treasury to stay continuously funded. Failed debits halt the epoch. |
| `claim` | Treasury debited only when the winner submits a signed claim. A ledger entry is created at epoch close. | Winners receive a webhook/notification and must submit a claim transaction. | Operators gain flexibility to schedule treasury top-ups and apply off-chain checks before approving payouts. |

### Auto Mode

* **When to use:** high-trust, low-latency environments (e.g. public marketing programs) where immediate settlement is
  more important than workflow control.
* **How it works:** once `maybeProcessPotsoRewards` finalises an epoch, the node subtracts the total paid amount from the
  configured treasury account and credits each winner. `potso.reward.paid` events are emitted immediately. A claim record is
  still written for audit purposes (`claimed=true`, `mode=auto`).
* **Operational guardrails:** keep the treasury topped up above the configured emission. If the balance cannot cover the
  computed payout the epoch fails with `potso.ErrInsufficientTreasury` and operators must fund the account before retrying.

### Claim Mode

* **When to use:** controlled payouts (e.g. loyalty rebates, ambassador programs) where finance/compliance teams want to run
  additional checks or batch treasury top-ups before releasing rewards.
* **How it works:** epoch processing stores a ledger entry per winner (`claimed=false`, `mode=claim`) and emits
  `potso.reward.ready` webhooks. No funds move at this stage. Winners (or downstream automation) call the
  `potso_reward_claim` RPC with a signature that proves ownership. The node debits the treasury at claim time, updates the
  ledger, appends the history entry, and emits `potso.reward.paid` with `mode=claim`.
* **Operational guardrails:** treasury must be funded before the claim executes. If insufficient balance exists the claim
  fails with `INSUFFICIENT_TREASURY` and the ledger remains `claimed=false`. Claims are idempotent; retries after funding the
  treasury succeed without double-paying.

## Configuration

The payout mode is configured in `config.toml`:

```toml
[potso.rewards]
PayoutMode = "auto"   # or "claim"
TreasuryAddress = "nhb1..."
EmissionPerEpochWei = "1000000000000000000000"
MinPayoutWei = "1000000000000000"
```

`config.PotsoRewardConfig()` normalises the value (`auto` becomes the default when the field is omitted). Mode changes take
effect immediately after the configuration is reloaded; the next epoch will follow the new settlement flow.

## Mode Switching Guidance

1. **Announce the change.** Notify stakeholders, wallet teams, and downstream services before switching modes.
2. **Drain the queue.** When moving from `claim` to `auto`, ensure that all outstanding claims are settled to avoid surprises
   when future exports show mixed modes.
3. **Monitor webhooks and history.** The new ledger records (`PotsoRewardsGetClaim`, `PotsoRewardsHistory`) expose the mode for
   every entry. Dashboards should surface the mode to differentiate auto vs. manual payouts.
4. **Update off-chain automation.** Claim mode requires downstream workers (bots, finance ops) to submit signed claims. CLI
   support is provided via `nhb-cli potso reward claim`.

## Operational Trade-offs

* **Cash management:** claim mode lets treasury teams bundle top-ups and apply manual review. Auto mode favours simplicity at
  the cost of needing larger standing balances.
* **User experience:** auto mode delivers instant gratification. Claim mode introduces an extra step but gives room for UI flows
  that verify KYC or prompt the user to update payout accounts.
* **Risk management:** claim mode is resilient to temporary treasury shortages because the ledger remains open until funds are
  replenished. Auto mode produces a hard failure when balances are insufficient.

## Failure Handling Checklist

| Scenario | Auto Mode Behaviour | Claim Mode Behaviour | Operator Action |
|----------|--------------------|----------------------|-----------------|
| Treasury balance below payout | Epoch processing aborts with `potso.ErrInsufficientTreasury`. | Claim attempts return `INSUFFICIENT_TREASURY` until funds arrive. | Fund treasury, rerun claim/epoch. |
| User retries claim | N/A (already paid). | Idempotent – `paid=false`, amount returned for transparency. | Inform user no additional action is needed. |
| Mode switched mid-series | New epochs adopt the new mode; historical entries retain their recorded mode. | Same. | Communicate expected behaviour; exports include the `mode` field for reconciliation. |

Keep the mode decision aligned with product goals, treasury governance, and customer expectations. Both paths are available at
runtime without redeploying the chain.
