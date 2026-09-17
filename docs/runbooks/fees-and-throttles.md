# Fees and throttles runbook

This runbook outlines the operational checks for the free-tier allowance and
merchant discount rate (MDR) throttles. Operators should reference it when they
need to validate the counters after a governance change or diagnose fee-related
alerts.

## Monthly free-tier allowance

* **Default window:** Wallets receive 100 sponsored transactions per calendar
  month. The counter aggregates NHB and ZNHB transfers by default, so any
  combination of assets shares the same allowance. Domains can opt into
  per-asset tracking by toggling the `FreeTierPerAsset` policy flag.
* **Counter key:** The node persists counters under `fees/counter/<domain>/<YYYYMM>/<scope>/<address>`.
  The scope is `__AGGREGATE__` when the allowance is shared or the normalized
  asset symbol when split per asset.
* **Override procedure:** Submit a governance proposal that updates the
  `free_tier_tx_count` parameter. Changes take effect once the proposal is
  accepted and the new policy is broadcast. Reference the
  [fee policy parameters guide](../governance/fee-params.md) for the CLI syntax.
* **Operational check:** Query the `fees.applied` events to verify that the
  `freeTierRemaining` field resets on the first UTC day of the month and that
  the 101st transaction in a period includes a non-zero fee.

## MDR thresholds

* **Default MDR:** 150 basis points (1.5%) for POS flows. Asset overrides in the
  policy map can route to different wallets or basis points.
* **Free-tier interaction:** MDR is only charged after the free-tier counter for
  the relevant scope is exhausted. Expect the first 100 transactions per wallet
  (across NHB and ZNHB) to emit `freeTierApplied=true` in the fee event stream.
* **Governance override:** Update `pos_mdr_bps` (or the per-asset override) via
  a governance proposal. Document the reason and effective epoch in the
  metadata field to aid post-mortems.

## Troubleshooting checklist

1. **Confirm the counter scope.** For unexpected charges, inspect whether the
   domain uses aggregate counters or per-asset tracking.
2. **Inspect recent events.** Pull the last 24 hours of `fees.applied` events to
   check the usage counter and remaining allowance for the affected wallet.
3. **Verify parameter state.** Run `nhbctl gov query params --module feepolicy`
   and compare `free_tier_tx_count` and `pos_mdr_bps` against the expected
   values.
4. **Escalate if counters stall.** If the allowance fails to reset at month
   boundaries, raise an incident and include the counter key, wallet address,
   and observed `windowStart` timestamp.
