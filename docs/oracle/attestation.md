# Oracle Attestation Service

The `oracle-attesterd` daemon ingests fiat settlement webhooks from
NOWPayments, verifies that the referenced ERC-20 transfer reached the treasury
collector account, and mints an on-chain `DepositVoucher` that records the
invoice. The service is designed to be idempotent – each `invoice_id` is minted
at most once even if the webhook is replayed.

## Webhook expectations

NOWPayments posts JSON payloads to `POST /np/webhook` with an HMAC signature in
`X-Nowpayments-Signature` (or `x-nowpayments-sig`). The service accepts the
payload when:

- The signature matches the configured `nowpayments_secret` (SHA-256 HMAC).
- `payment_status` is one of `finished`, `confirmed`, `confirming`, or
  `completed`.
- The payload specifies the invoice identifier in `invoice_id` (or `order_id`)
  and an ERC-20 transaction hash in either `transaction_hash` or
  `payment_details.tx_hash`.
- `pay_currency` maps to a configured asset symbol and the amount can be
  represented with the declared decimal precision.

Example payload:

```json
{
  "invoice_id": "np-123",
  "payment_status": "confirmed",
  "pay_currency": "USDC",
  "actually_paid": "250.00",
  "transaction_hash": "0xabc123...",
  "payment_details": {
    "token_address": "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
  },
  "created_at": "2024-04-05T12:18:01Z"
}
```

Requests exceeding 1 MiB, missing signatures, or containing malformed JSON are
rejected with `4xx` status codes.

## Settlement verification

`oracle-attesterd` connects to the configured EVM RPC (`evm.rpc_url`) and
verifies that the supplied transaction hash produced a successful receipt with a
`Transfer(address,address,uint256)` log emitted by the configured asset
contract, sending the expected amount to the collector address. Optional
`payment_details.token_address` values must also match the configured contract.

A mint is only accepted when the transaction has at least the configured number
of confirmations. If the receipt cannot be fetched, fails, or the log scan does
not find an exact amount match, the webhook returns `409 Conflict` and the
invoice reservation is released so that later retries can succeed.

## Secrets and key management

`oracle-attesterd` signs consensus submissions with the key referenced by
`signer_key_env`. The environment variable must contain a lowercase hexadecimal
encoding of the 32 byte secp256k1 private key. For development-only workflows
the legacy `signer_key` scalar is still accepted, but production deployments
should source the key from a secret manager (for example Kubernetes secrets or
HashiCorp Vault agents) and project it into the container environment. Rotating
the key only requires updating the secret and restarting the deployment.

## Voucher minting and idempotency

Every successfully validated webhook reserves the invoice identifier in the
local BoltDB-backed store. Once on-chain verification passes, the service:

1. Reserves a monotonically increasing nonce (starting from `nonce_start`).
2. Builds a `swap.v1.MsgMintDepositVoucher` message with:
   - `authority`: configured module authority.
   - `voucher.invoice_id`: webhook invoice.
   - `voucher.provider`: `NOWPAYMENTS` (or overridden `provider`).
   - `voucher.stable_asset` / `stable_amount`: canonical integer amount.
   - `voucher.nhb_amount`: mirrored stable amount.
   - `voucher.account`: `treasury_account`.
   - `voucher.memo`: settlement transaction hash.
   - `voucher.created_at`: webhook timestamp (seconds).
3. Wraps the message in a consensus transaction envelope (applying optional
   fee metadata) and signs it with the key resolved from `signer_key_env`
   before submitting to the
   consensus endpoint.

On success the invoice status is upgraded to `minted`, ensuring subsequent
webhooks for the same invoice return `200 OK` without resubmitting the voucher.
If consensus submission fails the reservation is released and the request
returns `502 Bad Gateway`, allowing upstream systems to retry.

## Replay protection and health

The service persists invoice reservations and the next consensus nonce in
`${database}` (BoltDB). Restarting the process preserves idempotency: already
minted invoices will always respond with `{"status":"minted"}`.

A simple `GET /healthz` endpoint returns `200 OK` for external monitoring.

## Failure modes

- **401 Unauthorized** – Missing or invalid HMAC header.
- **400 Bad Request** – Malformed JSON, unknown asset, or unsupported amount
  precision.
- **409 Conflict** – Settlement transaction not yet final or mismatched.
- **502 Bad Gateway** – Downstream consensus submission failed.

Operators should monitor logs for repeated conflicts (potentially indicating a
stuck settlement) and ensure the configured EVM endpoint tracks the collector
chain head closely enough for confirmation checks.
