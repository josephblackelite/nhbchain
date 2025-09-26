# Treasury Peg Policy

The treasury relies on a diversified oracle basket to anchor swap mint quotes and to monitor
pricing risk across custody and exchange venues.

## Oracle basket

- **Primary custody feeds** – Signed samples delivered by the fiat gateway from custody risk
  engines. These feeds are registered via `swap.ProviderStatus` and occupy the top of the
  aggregator priority list.
- **Secondary market data** – CoinGecko and other public APIs remain configured as
  lower-priority fallbacks. They automatically surface when the primary feeds deviate or fail
  freshness checks.
- **Manual breaker feed** – The on-call team can inject a signed manual quote via
  `SetSwapManualQuote`. Manual rates sit at the bottom of the priority stack and are only used
  during emergency circuit-breaker scenarios.

## Deviation and freshness guards

- **Max quote age** – `swap.MaxQuoteAgeSeconds` bounds staleness. Quotes older than the cap are
  rejected by the node and logged for investigation.
- **Median guardrails** – The fiat gateway enforces per-feed deviation thresholds and circuit
  breakers before samples are relayed to the chain. The on-chain TWAP calculator now persists the
  median, TWAP window, feeder set, and a deterministic `priceProofId` with every swap for audit
  replay.
- **Circuit breakers** – If the basket deviates from the rolling TWAP by more than the configured
  basis points, the gateway halts new vouchers and requires break-glass approval to resume.

## Signer rotation

1. Provision a new KMS keypair and record the address in the signer inventory.
2. Update the payments gateway configuration (`MinterKMSEnv`) and rotate the service. The gateway
   emits an audit log and new oracle samples should begin populating the on-chain feeder set.
3. Call `swap_provider_status` and `swap_voucher_get` to confirm the new signer is producing fresh
   samples and that the resulting `priceProofId` values reference the updated key.
4. Revoke access for the retired signer, archive the runbook in the security vault, and note the
   rotation in the treasury change log.

## Circuit-breaker triggers

- Sustained `swap.mint.proof` events showing a single feeder or stagnant TWAP window.
- Deviation alarms from dashboards exceeding the median/TWAP guard bands.
- Manual override events from operations indicating custody outages or extreme volatility.
