# Loyalty Operations Runbook

This runbook documents common operational workflows for the loyalty base reward
engine. Founder mainnet uses a treasury-funded base reward that credits the
spender on qualifying NHB commerce flows.

Qualified commerce flows should be the operational baseline for support and product
teams. Do not frame the protocol reward as a blanket rebate on arbitrary transfers.
The intended reward surface is merchant or commerce settlement, with merchant-funded
bonus programs layered on top where applicable.

## Verify Current Configuration

1. Query the chain via JSON-RPC:
   ```bash
   curl -s http://localhost:8080/nhb_getLoyaltyGlobalConfig | jq
   ```
2. Ensure the response includes:
   * `active: true`
   * `baseBps: 50` unless a custom override is required
   * `treasury` pointing at a funded `ZNHB` treasury wallet
3. Cross-check user meters when investigating support reports:
   ```bash
   curl -s http://localhost:8080/nhb_getLoyaltyBaseMeters \
     -d '{"address":"nhb1...","day":"2024-02-01"}' | jq
   ```

## Adjust the Reward Rate

1. Draft a governance proposal updating the loyalty global config.
2. Use `50` for the founder default of `0.50%`.
3. Confirm the next qualifying settlement emits `loyalty.base.accrued` with the
   updated `baseBps`.

## Pause or Resume Loyalty Rewards

1. Set `pauses.loyalty = true` to freeze loyalty payouts.
2. Resume with `pauses.loyalty = false`.
3. Confirm expected behaviour from the `loyalty.base.accrued` and
   `loyalty.base.skipped` event streams.

## Monitor Treasury Health

* Track the loyalty treasury balance with `nhb_getAccount` or Prometheus.
* Watch for `reason=treasury_insufficient` on `loyalty.base.skipped`.
* Alert before the treasury balance falls below projected reward demand.

## Cap and Support Guidance

* `CapPerTx` limits very large single-purchase rewards.
* `DailyCapUser` limits aggregate shopper earnings per UTC day.
* Support messaging should frame rewards as:
  `Earn 0.5% back in ZNHB, subject to treasury and reward caps.`
* For merchants running bonus programs, support should frame the total reward as:
  `protocol base reward + merchant-funded bonus reward`, both settled in `ZNHB`.

## Troubleshooting Checklist

1. **No rewards paid**: confirm the module is active and not paused.
2. **Rewards lower than expected**: inspect per-tx caps, daily caps, and
   treasury sufficiency.
3. **Unexpected recipient**: confirm the transaction path is a qualifying spend
   flow and that rewards are bound to the spender, not the merchant.
