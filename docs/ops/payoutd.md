# payoutd Operations Runbook

`payoutd` automates stablecoin redemptions by watching on-chain cash-out intents and
executing ERC-20 transfers from the treasury hot wallet. This document captures the
operational procedures required to safely run the service.

## Architecture overview

* **Intent ingestion** – payoutd polls the stable module for `CashOutIntent`s in the
  `pending` state. Each intent is validated against the configured soft inventory and
  per-asset daily cap before processing.
* **Treasury execution** – approved intents trigger an ERC-20 transfer from the
  `treasury_hot` address using the MPC/HSM signer. The daemon waits for the configured
  number of confirmations prior to emitting the receipt.
* **Attestation** – once finality is observed payoutd submits a `MsgPayoutReceipt`
  via the consensus client which finalises the intent and burns the escrowed NHB.
* **Controls** – operators interact with the admin HTTP API to pause processing,
  abort intents, or inspect current status. Prometheus metrics expose payout latency,
  cap utilisation, and error counters.

## Key management

* **Signer keys** – the attestation signer uses a secp256k1 key stored in the MPC/HSM
  service. Export the compressed private key as a hex string when generating the
  configuration. Rotate keys by updating the consensus authority account and restarting
  payoutd with the new secret.
* **Hot wallet** – ERC-20 transfers are executed using the `treasury_hot` key held in
  the custody HSM. MPC policies should enforce dual-operator approval and velocity
  limits aligned with payoutd's caps.
* **Cold wallet** – maintain a `treasury_cold` address with the majority of stable
  reserves. Fund the hot wallet using pre-signed transactions or MPC flows documented
  below. The service never has direct access to cold keys.

### Refilling the hot wallet

1. Forecast the required buffer using `cap_remaining` metrics.
2. Initiate a transfer from `treasury_cold` to `treasury_hot` via the custody MPC,
   referencing the payout queue for justification.
3. Wait for the transfer to reach the required confirmations and update the custody
   ledger.
4. Update payoutd's `inventory` override if the refill changes available balances.

## Policy management

Policies are defined in `services/payoutd/policies.yaml` and mirrored in production
configuration management. Each entry specifies:

* `asset` – stablecoin symbol (e.g. USDC, USDT).
* `daily_cap` – total integer units permitted per 24-hour rolling window.
* `soft_inventory` – maximum amount payable before manual review.
* `confirmations` – EVM confirmations required before emitting a receipt.

Reload policies by updating the configuration and restarting payoutd. The admin API's
`/status` endpoint surfaces current remaining caps for verification.

## Admin API security

Operators must authenticate every admin request with either a shared bearer token or a mutually-authenticated TLS
certificate. Configure the security block in `services/payoutd/config.yaml` (or the equivalent deployment manifest):

```yaml
admin:
  bearer_token: "${PAYOUTD_ADMIN_TOKEN}"
  tls:
    cert: /etc/payoutd/tls/tls.crt
    key: /etc/payoutd/tls/tls.key
  mtls:
    enabled: true
    client_ca: /etc/payoutd/tls/client-ca.crt
```

TLS is enabled automatically when certificate and key paths are supplied. Only disable TLS for ephemeral local testing by
setting `admin.tls.disable: true`. When mTLS is enabled, clients must present a certificate issued by the configured
`client_ca` bundle. Requests that fail authentication return `401 Unauthorized` before hitting the processor.

Example commands:

```bash
curl https://payoutd.example.com/status \
  -H 'Authorization: Bearer ${PAYOUTD_ADMIN_TOKEN}' \
  --cacert ca.pem
```

```bash
curl https://payoutd.example.com/pause \
  --cert ops-client.pem --key ops-client.key \
  --cacert ca.pem \
  -X POST
```

## Aborting a payout

Abort an intent when fiat settlement fails or the customer requests cancellation.

```bash
curl -X POST https://payoutd.example.com/abort \
  -H 'Authorization: Bearer ${PAYOUTD_ADMIN_TOKEN}' \
  -H 'Content-Type: application/json' \
  -d '{"intent_id":"intent-123","reason":"compliance_hold"}' \
  --cacert ca.pem
```

The daemon submits `MsgAbortCashOutIntent`, unlocking the escrowed NHB. The operator
should notify the customer and document the reason in the settlement tracker.

## Rollback and recovery

* **Pause processing** – issue `POST /pause` to halt new transfers while investigating
  incidents. `POST /resume` restarts processing once remediation is complete.
* **Replay safety** – payoutd records the last processed intent ID and skips duplicates,
  allowing safe restarts.
* **Partial failures** – if the service crashes after broadcasting a transfer but before
  emitting a receipt, the retry will detect the in-flight state and resume confirmation
  polling. Manual reconciliation is available via the admin status payload and the
  `payout_latency` histogram.
* **Disaster recovery** – redeploy payoutd in a new region by restoring the policy file,
  inventory overrides, and consensus signer. The MPC/HSM retains wallet state; ensure
  the network egress to the custody endpoint is permitted.

## Metrics and alerting

Expose the Prometheus endpoint and alert on:

* `nhb_payoutd_errors_total{reason="transfer"}` – spikes indicate wallet failures.
* `nhb_payoutd_cap_remaining` – alert when approaching zero to trigger treasury refills.
* `nhb_payoutd_payout_latency_seconds` – monitor for latency regressions.

Integrate these metrics into existing dashboards alongside fiat settlement KPIs to
maintain full visibility of redemption flows.
