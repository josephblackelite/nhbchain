# Loyalty Operations Runbook

This runbook documents common operational workflows for the loyalty base reward
engine. It focuses on the global configuration that powers the 0.5% default
reward and the observability hooks operators rely on to confirm payouts.

## Verify Current Configuration

1. Query the chain via JSON-RPC:
   ```bash
   curl -s http://localhost:8080/nhb_getLoyaltyGlobalConfig | jq
   ```
2. Ensure the response includes:
   * `active: true`
   * `baseBps: 50` unless a custom override is required.
   * `treasury` pointing at the funded ZNHB wallet.
3. Cross-check meters when investigating user reports:
   ```bash
   curl -s http://localhost:8080/nhb_getLoyaltyBaseMeters \
     -d '{"address":"nhb1...","day":"2024-02-01"}' | jq
   ```

The state processor automatically injects `baseBps: 50` when the stored config
leaves the field empty, so older genesis files remain compatible.

## Adjust the Reward Rate

1. Draft a governance proposal updating `loyalty.GlobalConfig` via
   `gov.v1.MsgSetGlobalConfig` (or the relevant multisig workflow).
2. Specify the desired `baseBps`. Use 50 for the 0.5% baseline; values above
   10,000 will be rejected during validation.
3. Submit and monitor the proposal lifecycle. Once applied, the next qualifying
   transaction will emit a `loyalty.base.accrued` event reflecting the new
   `baseBps` attribute.

## Pause or Resume Loyalty Rewards

1. Stage a `gov.v1.MsgSetPauses` transaction with `pauses.loyalty = true` to
   freeze payouts. When paused, the engine short-circuits before computing the
   reward and no events are emitted.
2. Resume by sending another `MsgSetPauses` request with `pauses.loyalty = false`.
3. Confirm behaviour by tailing events:
   ```bash
   nhbcli events --type loyalty.base.accrued
   ```
   Lack of new events during the pause window confirms the module halt.

## Monitor Treasury Health

* Use `nhb_getAccount` (or Prometheus metrics) to confirm the loyalty treasury
  maintains sufficient ZNHB. The engine emits `loyalty.base.skipped` events with
  `reason=treasury_insufficient` when balances drop below the computed reward.
* Configure alerts on the treasury balance threshold that matches projected
  earn volume and caps.

## Respond to Cap-Related Questions

* `CapPerTx` constrains rewards for large individual purchases. The event stream
  still reports the full `baseBps` so downstream systems can distinguish capped
  payouts.
* `DailyCapUser` limits aggregate earnings per shopper per UTC day. Investigate
  by querying the meters via `nhb_getLoyaltyBaseMeters`.
* Remind support teams that the 0.5% rate applies before caps; communications
  should phrase limits accordingly (e.g., “Earn 0.5% back up to 50 ZNHB per
  purchase”).

## Troubleshooting Checklist

1. **No rewards minted** – confirm the module is not paused and `Active` remains
   true.
2. **Rewards lower than expected** – check per-tx or daily caps, then inspect the
   treasury balance for insufficiencies.
3. **Events missing `baseBps`** – ensure nodes are running the updated binaries;
   the attribute now accompanies every `loyalty.base.accrued` event.
