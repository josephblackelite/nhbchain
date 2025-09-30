# Mint Settlement RPC (`mint_with_sig`)

## Overview
The `mint_with_sig` JSON-RPC method finalises invoice-backed mints for both **NHB** and **ZNHB** tokens. The payments gateway
obtains a signed voucher from an authorised minter (e.g. the NowPayments workflow) and submits it to the node. The node
verifies the signature, packages the payload into a dedicated `TxTypeMint` transaction, and queues it in the mempool so block
execution credits the recipient while emitting an auditable `mint.settled` event.

This flow keeps mint authority centralised to wallets with the `MINTER_NHB` or `MINTER_ZNHB` roles and provides a deterministic
bridge between fiat settlements and on-chain supply adjustments.

## Voucher schema
| Field       | Type   | Description |
|-------------|--------|-------------|
| `invoiceId` | string | Unique identifier for the fiat invoice. Replays are rejected once persisted. |
| `recipient` | string | NHB bech32 address or username. Usernames resolve through the identity module; otherwise the value must be an address. |
| `token`     | string | Token symbol (`NHB` or `ZNHB`). Determines the required minter role. |
| `amount`    | string | Base unit amount (integer). Must be strictly positive. |
| `chainId`   | number | Must equal `187001` (`core.MintChainID`). Prevents cross-network replay. |
| `expiry`    | number | UNIX timestamp (seconds). Must be greater than the node's current time when processed. |

All string values are trimmed before processing and token symbols are normalised to upper-case.

## Canonical signing payload
* Canonical JSON is generated from the voucher with the fields above in the order shown.
* The JSON bytes are hashed with `keccak256` and signed using secp256k1 (`ethcrypto.Sign`).
* The signature is provided to the RPC method as a hex string (optionally prefixed with `0x`).
* The node recovers the signer with `SigToPub` and verifies the corresponding address holds the required role:
  * `NHB` -> `MINTER_NHB`
  * `ZNHB` -> `MINTER_ZNHB`

The payments gateway embeds this canonicalisation in `core.MintVoucher.CanonicalJSON`, ensuring every client signs the same
payload auditors will verify.

## Replay protection and persistence
* Every successful mint records the `invoiceId` inside the state trie via `state.MintInvoiceKey`.
* Subsequent submissions with the same invoice are rejected with `ErrMintInvoiceUsed` and a `codeDuplicateTx` RPC error. The
  mempool also rejects pending duplicates to avoid block construction failures.
* Replays or attempts with expired vouchers (`ErrMintExpired`), wrong chain IDs (`ErrMintInvalidChainID`), or malformed payloads
  (`ErrMintInvalidPayload`) are surfaced as `codeInvalidParams` errors.

## Event emission
A successful mint appends a `mint.settled` event:

```
{
  "type": "mint.settled",
  "attributes": {
    "invoiceId": "inv-123",
    "recipient": "nhb1...",
    "token": "NHB",
    "amount": "2500000000000000000",
    "txHash": "0xabc123..."
  }
}
```

The `txHash` is derived from the canonical voucher payload and signature, providing downstream systems with a deterministic
identifier for reconciliation.

## Settlement flow with NowPayments
1. A fiat invoice is created through NowPayments and stored in the payments gateway database.
2. Once the webhook reports a finished payment, the gateway constructs a `core.MintVoucher` with:
   * `invoiceId` set to the internal invoice record.
   * `chainId` fixed to `187001` and `expiry` set to `now + 10 minutes` (configurable via `mintVoucherTTL`).
   * Token amount sourced from the locked quote.
3. The gateway signs the canonical JSON using the configured KMS key (holding the appropriate minter role).
4. The RPC client submits `mint_with_sig(voucherJSON, signatureHex)` to the node.
5. The node validates the voucher, enqueues a `TxTypeMint` transaction, and returns a deterministic `txHash` derived from the
   voucher payload and signature.
6. When a block including that transaction executes, validators credit the recipient (address or resolved username), record the
   invoice usage, and emit `mint.settled`.
7. The gateway records the returned `txHash` against the invoice, marking it as `minted` for downstream reporting once the block
   is confirmed.

This alignment provides auditors and partners with a full trail from fiat settlement through to on-chain emission.

## Audience notes
### Auditors & Risk Teams
* Replay-protected invoice ledger enables deterministic reconciliation.
* Event payloads include recipient bech32 addresses and amounts for ledger matching.
* Only addresses with explicit `MINTER_*` roles can authorise mints; role assignments live in the state trie.

### Investors & Treasury
* Chain ID guard rails prevent cross-network leakage of supply adjustments.
* Expiring vouchers limit exposure to stale authorisations.
* `mint.settled` events act as real-time notifications for treasury dashboards.

### Customers & Support
* Usernames are seamlessly resolved when the identity module is enabled; otherwise requests must provide a bech32 address.
* Duplicate or expired vouchers return descriptive errors that can be surfaced to support tooling.

### Developers & Integrators
* Use `core.MintVoucher` helpers to build and sign vouchers to avoid subtle normalisation mismatches.
* RPC request example:

```
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "mint_with_sig",
  "params": [
    {
      "invoiceId": "inv-123",
      "recipient": "nhb1alice...",
      "token": "NHB",
      "amount": "2500000000000000000",
      "chainId": 187001,
      "expiry": 1733070300
    },
    "0x...signature..."
  ]
}
```

* Response:

```
{"jsonrpc":"2.0","id":7,"result":{"txHash":"0xdeadbeef..."}}
```

* On failure the node returns an `error` object with the mappings described above. If the node's mempool is full the RPC
  responds with `codeMempoolFull`; clients should retry once capacity is available.
