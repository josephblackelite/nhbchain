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
  abort intents, manage risk holds, or inspect current status. Prometheus metrics
  expose payout latency, cap utilisation, and error counters.

## Key management

* **Signer keys** – the attestation signer uses a secp256k1 key stored in the MPC/HSM
  service. Export the compressed private key as a hex string and load it via the
  `signer_key_env` environment variable when generating configuration. Rotate keys by
  updating the consensus authority account, rotating the secret in the manager (for
  example Kubernetes secrets or Vault), and restarting payoutd with the new secret
  projected into the environment. The legacy inline `signer_key` scalar remains for
  local testing only.
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
configuration management. Each entry can now specify:

* `asset` – stablecoin symbol (e.g. USDC, USDT).
* `daily_cap` – total integer units permitted per 24-hour rolling window.
* `soft_inventory` – maximum amount payable before manual review.
* `confirmations` – EVM confirmations required before emitting a receipt.
* `per_user_daily_cap` – amount a single NHB account can cash out per day.
* `per_user_hourly_cap` – amount a single NHB account can cash out per hour.
* `per_destination_daily_cap` – amount a single destination wallet can receive per day.
* `max_payouts_per_hour` / `max_payouts_per_day` – velocity controls for repeated redemptions.
* `require_review_above` – threshold above which operator approval is mandatory.
* `require_review_for_regions` / `require_review_for_partners` – high-risk routing controls.
* `blocked_destinations`, `blocked_accounts`, `blocked_regions`, `blocked_partners` – static screening lists.
* `allowed_destination_prefixes` – simple route allowlisting for supported destination formats.

Reload policies by updating the configuration and restarting payoutd. The admin API's
`/status` endpoint surfaces current remaining caps for verification.

## Treasury wallet configuration

`payoutd` now requires an explicit EVM treasury wallet configuration. The service
refuses to start if the wallet signer, chain ID, RPC endpoint, or asset routes are
missing or inconsistent.

```yaml
treasury_store: "nhb-data-local/payoutd/treasury.db"
execution_store: "nhb-data-local/payoutd/executions.db"
hold_store: "nhb-data-local/payoutd/holds.db"

wallet:
  rpc_url: "https://ethereum-rpc.example"
  chain_id: "1"
  signer_key_env: "PAYOUTD_WALLET_KEY"
  from_address: "0xTreasuryHotWallet"
  confirmations: 3
  poll_interval: "3s"
  assets:
    - symbol: "USDC"
      token_address: "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
      cold_address: "0xTreasuryColdWallet"
      hot_min_balance: "25000000000"
      hot_target_balance: "50000000000"
    - symbol: "USDT"
      token_address: "0xdAC17F958D2ee523a2206206994597C13D831ec7"
      cold_address: "0xTreasuryColdWallet"
      hot_min_balance: "25000000000"
      hot_target_balance: "50000000000"
```

Supported wallet routes:

* `token_address` routes the asset through an ERC-20 transfer.
* `native: true` routes the asset through a native EVM value transfer.
* `cold_address` defines the destination for excess hot-wallet sweeps.
* `hot_min_balance` defines the minimum safe hot-wallet balance before operators should refill.
* `hot_target_balance` defines the steady-state hot-wallet target used for refill and sweep recommendations.

At startup, payoutd validates that:

* the signer key resolves from `signer_key`, `signer_key_env`, or `signer_key_file`
* the derived signer address matches `from_address` when provided
* the configured `chain_id` matches the connected RPC node
* every asset symbol has exactly one valid route

Before submitting a payout, payoutd also performs a live on-chain balance check for the
requested asset. If the hot wallet cannot actually cover the payout, the request is
rejected before broadcast instead of failing later in the treasury leg.

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

Example `/status` response:

```json
{
  "paused": false,
  "processed": 12,
  "aborted": 1,
  "in_flight": 0,
  "cap_remaining": {
    "USDC": "499999000000",
    "USDT": "500000000000"
  },
  "wallet": {
    "mode": "evm",
    "rpc_url": "https://ethereum-rpc.example",
    "chain_id": "1",
    "from_address": "0xTreasuryHotWallet",
    "assets": {
      "USDC": {
        "native": false,
        "token_address": "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
      },
      "USDT": {
        "native": false,
        "token_address": "0xdAC17F958D2ee523a2206206994597C13D831ec7"
      }
    }
  }
}
```

Additional treasury-control endpoints:

* `GET /executions` returns persisted payout execution rows, optionally filtered by `status`, `asset`, and `limit`.
* `GET /holds` returns active or historical risk/compliance holds, optionally filtered by `scope` and `active=true`.
* `POST /holds` creates an active hold on an `account`, `destination`, `partner`, or `region`.
* `POST /holds/release` clears a hold while preserving the audit trail.
* `GET /treasury/reconcile` returns a per-asset treasury snapshot including on-chain hot-wallet balances, remaining soft inventory, remaining daily cap, configured hot/cold thresholds, and the recommended action (`none`, `refill_hot`, `sweep_to_cold`, or `inspect_wallet`).
* `GET /treasury/sweep-plan` returns only the subset of assets that currently require operator action.
* `POST /treasury/instructions` creates a persistent treasury instruction (`refill_hot` or `sweep_to_cold`) with the intended amount and source/destination routing.
* `POST /treasury/instructions/review` approves or rejects a pending treasury instruction. The reviewer must be different from the requester (maker-checker).
* `GET /treasury/instructions?status=pending` lists persisted treasury instructions for operations and audit teams.

Example `/treasury/reconcile` response:

```json
{
  "generated_at": "2026-04-12T10:00:00Z",
  "assets": [
    {
      "asset": "USDC",
      "native": false,
      "token_address": "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
      "cold_address": "0xTreasuryColdWallet",
      "daily_cap_remaining": "100000000000",
      "soft_inventory_remaining": "50000000000",
      "on_chain_balance": "20000000000",
      "hot_min_balance": "25000000000",
      "hot_target_balance": "50000000000",
      "coverage_delta": "-30000000000",
      "action": "refill_hot",
      "recommended_amount": "30000000000",
      "healthy": false
    }
  ]
}
```

Example treasury instruction:

```json
{
  "id": "ti-123",
  "action": "sweep_to_cold",
  "asset": "USDC",
  "amount": "30000000000",
  "source": "0xTreasuryHotWallet",
  "destination": "0xTreasuryColdWallet",
  "status": "pending",
  "requested_by": "ops-maker-1",
  "notes": "move excess hot balance",
  "created_at": "2026-04-12T10:05:00Z"
}
```

Example payout execution:

```json
{
  "intent_id": "intent-123",
  "account": "nhb1merchant",
  "partner_id": "partner-west",
  "region": "uae",
  "requested_by": "ops-user-1",
  "approved_by": "checker-1",
  "approval_ref": "case-123",
  "stable_asset": "USDC",
  "stable_amount": "100000000",
  "nhb_amount": "100000000",
  "destination": "0xUserWallet",
  "evidence_uri": "urn:nhb:payout:intent-123",
  "tx_hash": "0xabc123",
  "status": "settled",
  "created_at": "2026-04-12T10:10:00Z",
  "updated_at": "2026-04-12T10:12:00Z",
  "settled_at": "2026-04-12T10:12:00Z"
}
```

Example hold:

```json
{
  "id": "hold-123",
  "scope": "destination",
  "value": "0xUserWallet",
  "reason": "sanctions review",
  "created_by": "risk-ops-1",
  "created_at": "2026-04-12T11:00:00Z",
  "updated_at": "2026-04-12T11:00:00Z",
  "active": true
}
```

The instruction flow is intentionally maker-checker:

1. A treasury operator creates the refill or sweep instruction.
2. A different operator reviews it through `/treasury/instructions/review`.
3. The approved instruction becomes the audit record for the manual MPC / custody execution.

This does not yet broadcast cold-wallet movements automatically. The service's role is
to make treasury actions explicit, reviewable, and queryable before execution is handed
to the custody environment.

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
