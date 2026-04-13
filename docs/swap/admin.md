# Swap Mint Admin Runbook

This guide is for operations, treasury, and compliance staff managing the SWAP-4 mint pipeline. It covers reversal policy, admin-only RPCs, and day-to-day responsibilities.

## Responsibilities

* **Operations** – Monitor `swap.alert.*` events, run `swap_limits` for escalations, and coordinate with PSPs when limits trip.
* **Treasury** – Maintain balances in the refund sink address to cover customer clawbacks and reconcile the `swap.alert.limit_hit` stream daily.
* **Compliance** – Periodically review sanctions hook integrations and ensure the provider allow list matches the approved PSP roster.

## Reversal Policy

Reversals are an emergency tool for clawing back mistaken mints.

* Only vouchers in `minted` status can be reversed.
* The recipient’s custodial balance must cover the mint amount; otherwise the request fails.
* Reversals transfer funds from the recipient to the configured refund sink. No burn is performed—funds stay on-chain for auditors.
* Reversal attempts are idempotent. A second call returns `{ "ok": true }` if the voucher is already reversed.

Document the customer ticket, PSP communication, and reason for reversal in the compliance system before executing the command.

## Admin RPC Endpoints

All admin endpoints require the bearer token (`NHB_RPC_TOKEN`).

### `swap_limits`

Retrieve counters and remaining room for a customer address.

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "swap_limits",
  "params": ["nhb1customer000000000000000000000000000"]
}
```

Response fields:

* `day` and `month` – UTC buckets with minted totals.
* `dayRemainingWei` / `monthRemainingWei` – Remaining room when caps are enabled.
* `velocity` – Observed mints inside the configured window and remaining allowance.

### `swap_provider_status`

Reports the provider allow list and the timestamp of the last successful oracle health check.

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "swap_provider_status",
  "params": []
}
```

Use this endpoint in dashboards to confirm that PSP integrations remain enabled.

### `swap_voucher_reverse`

Reverse a voucher by provider transaction identifier.

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "swap_voucher_reverse",
  "params": ["order-12345"]
}
```

Checklist before executing:

1. Confirm the fiat transaction has been refunded or voided with the PSP.
2. Verify the customer’s on-chain balance covers the mint.
3. Ensure the refund sink wallet is controlled by treasury.
4. Run the command and record the RPC response, event hash, and the internal ticket ID.

If the command returns an error, inspect the JSON error message for guidance:

* `daily limit exceeded` – the voucher was already processed under a new identifier.
* `insufficient balance` – the custodial account lacks funds; coordinate with treasury.
* `voucher not found` – confirm the `providerTxId` matches the PSP record exactly.

## Incident Response

* Spike in `swap.alert.velocity` – confirm PSP behaviour, temporarily raise `VelocityMaxMints` if needed, and log the change.
* Sanctions alert – freeze the account, notify compliance, and coordinate with the sanctions provider.
* Repeated provider rejections – verify the allow list matches the operational roster and update `[swap.providers]` if a new PSP is onboarded.

Maintain a weekly audit of reversal activity by exporting the voucher ledger and filtering for `status = reversed`.
