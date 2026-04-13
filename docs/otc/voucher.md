# OTC Voucher Submission

The OTC gateway creates on-chain mint vouchers once invoices are approved by compliance. This document describes the payload schema, idempotency guarantees, and lifecycle enforced by `/ops/otc/invoices/{id}/sign-and-submit`.

## Payload Schema

The gateway constructs a `core.MintVoucher` object with the following fields:

| Field | Description |
| --- | --- |
| `invoiceId` | UUID of the OTC invoice. Doubles as the default `providerTxId`. |
| `recipient` | NHB Bech32 recipient address provided by operations. |
| `token` | Asset symbol (defaults to `NHB`). |
| `amount` | Decimal string representing the amount in wei. Must be positive. |
| `chainId` | Parsed from `OTC_CHAIN_ID`. |
| `expiry` | UNIX timestamp computed as `now + OTC_VOUCHER_TTL_SECONDS`. |

The canonical JSON is hashed with keccak256, signed by the HSM, and submitted to `swap_submitVoucher` alongside a deterministic `providerTxId`.

The RPC payload is wrapped in a `swaprpc.MintSubmission` object that also carries compliance metadata when a partner generated the invoice. The `compliance` envelope includes:

| Field | Description |
| --- | --- |
| `partnerDid` | Decentralized identifier resolved via the identity service. |
| `complianceTags` | Attestations returned by the identity service. The gateway requires at least one Travel Rule tag before signing. |
| `travelRulePacket` | Raw JSON payload recorded for validator desks or travel-rule side channels. |
| `sanctionsStatus` | Normalized sanctions decision (e.g. `clear`, `blocked`). |

The voucher, signature, provider metadata, and compliance envelope are forwarded to the swap RPC and persisted on both the invoice and voucher records for auditability.

## Workflow

1. **Validation** – The invoice must be in `APPROVED`. Maker-checker rules ensure the signer differs from both the creator and the approving supervisor. Branch exposure is recalculated to enforce per-branch caps in addition to the regional checks performed during approval.
2. **Signing** – The digest is sent to the HSM using mTLS. The signature and signer DN are persisted.
3. **Submission** – The voucher and signature are sent to `https://api.nhbcoin.net` (configurable via `OTC_SWAP_RPC_BASE`). The RPC response returns a transaction hash and an immediate `minted` flag.
4. **State transitions** – Invoices move to `SUBMITTED` when the RPC accepts the voucher. If `minted=true`, the invoice immediately transitions to `MINTED`. Otherwise a background poller queries `swap_voucher_get` until the mint finalizes.
5. **Audit trail** – The service appends `invoice.signed`, `invoice.submitted`, and `invoice.minted` events containing the voucher hash, signer DN, provider transaction ID, and transaction hash.

When the invoice was created by a partner contact, the gateway performs an identity lookup before the signing transaction executes. The DID must be verified, the sanctions status must not be blocked, and at least one Travel Rule attestation must be present. The resolved DID, sanctions decision, compliance tags, and Travel Rule packet are persisted on `models.Invoice` and `models.Voucher` and echoed to the validator desks via the RPC metadata.

## Idempotency

- `providerTxId` defaults to the invoice UUID, guaranteeing uniqueness across retries. Clients may supply their own deterministic ID via `provider_tx_id` to coordinate with upstream systems.
- `models.Voucher` enforces a unique index on `provider_tx_id` and `invoice_id`. Replays return the original signature, status, and transaction hash without re-signing or re-submitting.
- The HTTP layer still honours `Idempotency-Key` for end-to-end safety, but the voucher table prevents duplicate on-chain submissions even if headers are omitted.

## Voucher TTL

The signing endpoint respects `OTC_VOUCHER_TTL_SECONDS` (default 900 seconds). The expiry is encoded into the voucher and stored on the record. The mint poller will stop watching for `MINTED` events once the TTL plus a five-minute buffer elapses.

## Maker-Checker Enforcement

The endpoint rejects attempts where the submitting superadmin is also the invoice creator or the approving supervisor. Combined with audit events and signer DN persistence, this guarantees every mint passes through separate human reviewers and a hardware-backed signer.
