# Treasury Dashboards

The treasury dashboards surface swap oracle health, proof freshness, and redemption status to
support peg monitoring and operational readiness.

## Core panels

- **Proof freshness** – Track the latest `priceProofId` timestamps from `swap_voucher_list`. Alert
  if no new proof lands within two TWAP windows or if the feeder set collapses to a single signer.
- **Deviation bands** – Chart TWAP rate versus median and custody reference rates. Highlight periods
  when deviation exceeds configured guard rails or when manual feeds drive the price.
- **Feeder participation** – Display the feeder sets reported in `swap.mint.proof` events, including
  counts per signer and time since last observation.
- **Redeem settlement** – List recent `swap.redeem.proof` events showing the vouchers and proof IDs
  reconciled by treasury burns. Flag burns missing proof references for manual review.

## Alerting hooks

- Page on stale TWAP observations, missing median values, or proof IDs older than the configured SLA.
- Escalate when redeems land without matching proof IDs or when burn receipts accumulate without
  reconciliation events.
- Notify risk when deviation spikes coincide with manual feeder usage or when proof IDs oscillate
  between signers more frequently than rotation policy allows.
